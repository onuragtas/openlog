/*
 * Datastores: PDO, mysqli (procedural + OO), pgsql, phpredis, Predis. Client spans (kind 3) with db.system.name,
 * db.namespace, server.address/port, db.operation.name, db.collection.name, db.query.text.
 * SPDX-License-Identifier: Apache-2.0
 */
#include "ol.h"
#include "ext/pdo/php_pdo_driver.h"

#include <ctype.h>

typedef struct {
	const char *host;
	const char *db;
	int64_t port;
} ol_conn;

typedef struct {
	const char *sql;
	size_t len;
	zend_ulong link;
} ol_stmt;

static zend_ulong ol_handle_key(zval *z)
{
	if (z == NULL) {
		return 0;
	}
	if (Z_TYPE_P(z) == IS_OBJECT) {
		return (zend_ulong) Z_OBJ_HANDLE_P(z);
	}
	if (Z_TYPE_P(z) == IS_RESOURCE) {
		return ((zend_ulong) 1 << 40) | (zend_ulong) Z_RES_HANDLE_P(z);
	}
	return 0;
}

static HashTable *ol_table(HashTable **t)
{
	if (*t == NULL) {
		ALLOC_HASHTABLE(*t);
		zend_hash_init(*t, 8, NULL, NULL, 0);
	}
	return *t;
}

static ol_conn *conn_get(zend_ulong key, bool create)
{
	ol_conn *c;
	if (key == 0) {
		return NULL;
	}
	c = OLG(conns) ? zend_hash_index_find_ptr(OLG(conns), key) : NULL;
	if (c == NULL && create) {
		c = ol_alloc(sizeof(ol_conn));
		if (c) {
			memset(c, 0, sizeof(*c));
			zend_hash_index_update_ptr(ol_table(&OLG(conns)), key, c);
		}
	}
	return c;
}

static const char *zstr_copy(zval *z, size_t max)
{
	return (z && Z_TYPE_P(z) == IS_STRING) ? ol_strdup(Z_STRVAL_P(z), Z_STRLEN_P(z), max) : NULL;
}

static void conn_attrs(ol_node *n, const char *system, ol_conn *c)
{
	ol_attr_static(n, "db.system.name", system);
	if (c == NULL) {
		return;
	}
	if (c->host && *c->host) {
		ol_attr_static(n, "server.address", c->host);
	}
	if (c->port > 0) {
		ol_attr_int(n, "server.port", c->port);
	}
	if (c->db && *c->db) {
		ol_attr_static(n, "db.namespace", c->db);
	}
}

static void db_error_end(zend_execute_data *ex, zval *rv, bool false_is_error)
{
	ol_node *n = ol_span_end(ex);
	if (n == NULL) {
		return;
	}
	if (OL_EXCEPTION()) {
		ol_record_exception(n, EG(exception), true);
		ol_attr_str(n, "error.type", ZSTR_VAL(EG(exception)->ce->name), ZSTR_LEN(EG(exception)->ce->name));
	} else if (false_is_error && rv && Z_TYPE_P(rv) == IS_FALSE) {
		n->status = OL_STATUS_ERROR;
		ol_attr_static(n, "error.type", "_OTHER");
	}
}

static void db_client_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	db_error_end(ex, rv, true);
}

/* ---------------- PDO ---------------- */

static const char *pdo_system(pdo_dbh_t *dbh)
{
	const char *d = dbh && dbh->driver ? dbh->driver->driver_name : NULL;
	if (d == NULL) return "other_sql";
	if (strcmp(d, "mysql") == 0) return "mysql";
	if (strcmp(d, "pgsql") == 0) return "postgresql";
	if (strcmp(d, "sqlite") == 0) return "sqlite";
	if (strcmp(d, "sqlsrv") == 0 || strcmp(d, "dblib") == 0 || strcmp(d, "mssql") == 0) return "microsoft.sql_server";
	if (strcmp(d, "oci") == 0) return "oracle.db";
	if (strcmp(d, "firebird") == 0) return "firebird";
	return "other_sql";
}

/* DSN body (after "driver:"): key=value pairs separated by ';' (or spaces for pgsql). */
static void pdo_dsn_attrs(ol_node *n, const char *system, const char *dsn, size_t len)
{
	size_t i = 0;
	if (dsn == NULL) {
		return;
	}
	if (strcmp(system, "sqlite") == 0) {
		ol_attr_str(n, "db.namespace", dsn, len > 512 ? 512 : len);
		return;
	}
	while (i < len) {
		size_t ks, ke, vs, ve;
		while (i < len && (dsn[i] == ';' || isspace((unsigned char) dsn[i]))) i++;
		ks = i;
		while (i < len && dsn[i] != '=' && dsn[i] != ';') i++;
		ke = i;
		if (i >= len || dsn[i] != '=') {
			continue;
		}
		i++;
		vs = i;
		while (i < len && dsn[i] != ';' && !(isspace((unsigned char) dsn[i]) && strcmp(system, "postgresql") == 0)) i++;
		ve = i;
		if (ke - ks == 4 && strncasecmp(dsn + ks, "host", 4) == 0) {
			ol_attr_str(n, "server.address", dsn + vs, ve - vs > 256 ? 256 : ve - vs);
		} else if ((ke - ks == 6 && strncasecmp(dsn + ks, "server", 6) == 0)) {
			/* sqlsrv: Server=tcp:host,1433 */
			const char *v = dsn + vs;
			size_t vl = ve - vs;
			const char *comma;
			if (vl > 4 && strncasecmp(v, "tcp:", 4) == 0) { v += 4; vl -= 4; }
			comma = memchr(v, ',', vl);
			if (comma) {
				ol_attr_int(n, "server.port", ZEND_STRTOL(comma + 1, NULL, 10));
				vl = (size_t) (comma - v);
			}
			ol_attr_str(n, "server.address", v, vl > 256 ? 256 : vl);
		} else if (ke - ks == 4 && strncasecmp(dsn + ks, "port", 4) == 0) {
			ol_attr_int(n, "server.port", ZEND_STRTOL(dsn + vs, NULL, 10));
		} else if ((ke - ks == 6 && strncasecmp(dsn + ks, "dbname", 6) == 0) || (ke - ks == 8 && strncasecmp(dsn + ks, "database", 8) == 0)) {
			ol_attr_str(n, "db.namespace", dsn + vs, ve - vs > 256 ? 256 : ve - vs);
		} else if (ke - ks == 11 && strncasecmp(dsn + ks, "unix_socket", 11) == 0) {
			ol_attr_str(n, "server.address", dsn + vs, ve - vs > 256 ? 256 : ve - vs);
		}
	}
}

static void pdo_span(zend_execute_data *ex, pdo_dbh_t *dbh, const char *sql, size_t len)
{
	const char *system = pdo_system(dbh);
	uint32_t idx = ol_span_begin(ex, system, OL_KIND_CLIENT);
	ol_node *n = ol_node_at(idx);
	if (n == NULL) {
		return;
	}
	ol_attr_static(n, "db.system.name", system);
	if (dbh && dbh->data_source) {
		pdo_dsn_attrs(n, system, dbh->data_source, dbh->data_source_len);
	}
	ol_db_query_attrs(n, system, sql, len);
}

/* PDO::exec($sql), PDO::query($sql) */
static void pdo_dbh_begin(zend_execute_data *ex, const ol_hook *h)
{
	zval *self = ol_this(ex), *sql = ol_arg(ex, 1);
	pdo_dbh_t *dbh;
	if (self == NULL) {
		return;
	}
	dbh = Z_PDO_DBH_P(self);
	pdo_span(ex, dbh, sql && Z_TYPE_P(sql) == IS_STRING ? Z_STRVAL_P(sql) : NULL, sql && Z_TYPE_P(sql) == IS_STRING ? Z_STRLEN_P(sql) : 0);
}

/* PDOStatement::execute() */
static void pdo_stmt_begin(zend_execute_data *ex, const ol_hook *h)
{
	zval *self = ol_this(ex);
	pdo_stmt_t *stmt;
	if (self == NULL) {
		return;
	}
	stmt = Z_PDO_STMT_P(self);
#if PHP_VERSION_ID >= 80100
	pdo_span(ex, stmt->dbh, stmt->query_string ? ZSTR_VAL(stmt->query_string) : NULL, stmt->query_string ? ZSTR_LEN(stmt->query_string) : 0);
#else
	pdo_span(ex, stmt->dbh, stmt->query_string, stmt->query_stringlen);
#endif
}

/* PDO::prepare(): a span only when it fails (native prepares); successful prepares are part of execute. */
static void pdo_prepare_begin(zend_execute_data *ex, const ol_hook *h)
{
	pdo_dbh_begin(ex, h);
}

static void pdo_prepare_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	ol_frame *top = ol_stack_top();
	uint32_t idx;
	if (top == NULL || top->ex != ex || top->type != OL_FT_SPAN) {
		return;
	}
	idx = top->node;
	if (OL_EXCEPTION() || (rv && Z_TYPE_P(rv) == IS_FALSE)) {
		db_error_end(ex, rv, true);
		return;
	}
	ol_span_end(ex);
	ol_node_discard(idx);
}

/* ---------------- mysqli ---------------- */

#define MY_THIS  0x100 /* link is $this */
#define MY_RV    0x200 /* link/statement is the return value */
#define MY_BASE(a) ((a) & 0xff)

static zval *my_link(zend_execute_data *ex, const ol_hook *h, zval *rv)
{
	if (h->arg & MY_RV) return rv;
	if (h->arg & MY_THIS) return ol_this(ex);
	return ol_arg(ex, 1);
}

/* mysqli_connect / mysqli::__construct / connect / real_connect / mysqli_real_connect (host, user, pw, db, port) */
static void mysqli_connect_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	zval *link = my_link(ex, h, rv), *host, *db, *port;
	uint32_t base = MY_BASE(h->arg);
	ol_conn *c;
	if (link == NULL || Z_TYPE_P(link) != IS_OBJECT) {
		return;
	}
	c = conn_get(ol_handle_key(link), true);
	if (c == NULL) {
		return;
	}
	host = ol_arg(ex, base);
	db = ol_arg(ex, base + 3);
	port = ol_arg(ex, base + 4);
	c->host = zstr_copy(host, 256);
	if (c->host && strncmp(c->host, "p:", 2) == 0) {
		c->host += 2; /* persistent connection prefix */
	}
	if (c->host == NULL || *c->host == '\0') {
		c->host = "localhost";
	}
	c->db = zstr_copy(db, 256);
	c->port = port && Z_TYPE_P(port) == IS_LONG ? Z_LVAL_P(port) : 3306;
}

static void mysqli_select_db_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	zval *link = my_link(ex, h, rv), *db = ol_arg(ex, MY_BASE(h->arg));
	ol_conn *c = conn_get(ol_handle_key(link), true);
	if (c && rv && Z_TYPE_P(rv) == IS_TRUE) {
		c->db = zstr_copy(db, 256);
	}
}

/* query / real_query / multi_query / execute_query */
static void mysqli_query_begin(zend_execute_data *ex, const ol_hook *h)
{
	zval *link = my_link(ex, h, NULL), *sql = ol_arg(ex, MY_BASE(h->arg));
	uint32_t idx = ol_span_begin(ex, "mysql", OL_KIND_CLIENT);
	ol_node *n = ol_node_at(idx);
	if (n == NULL) {
		return;
	}
	conn_attrs(n, "mysql", conn_get(ol_handle_key(link), false));
	if (sql && Z_TYPE_P(sql) == IS_STRING) {
		ol_db_query_attrs(n, "mysql", Z_STRVAL_P(sql), Z_STRLEN_P(sql));
	}
}

static void stmt_store(zend_ulong key, zval *sql, zend_ulong link)
{
	ol_stmt *s;
	if (key == 0) {
		return;
	}
	s = OLG(stmts) ? zend_hash_index_find_ptr(OLG(stmts), key) : NULL;
	if (s == NULL) {
		s = ol_alloc(sizeof(ol_stmt));
		if (s == NULL) {
			return;
		}
		memset(s, 0, sizeof(*s));
		zend_hash_index_update_ptr(ol_table(&OLG(stmts)), key, s);
	}
	if (sql && Z_TYPE_P(sql) == IS_STRING) {
		s->sql = ol_strdup(Z_STRVAL_P(sql), Z_STRLEN_P(sql), OL_STR_MAX);
		s->len = s->sql ? strlen(s->sql) : 0;
	}
	if (link) {
		s->link = link;
	}
}

/* mysqli_prepare($link, $q) / mysqli::prepare($q): statement = return value */
static void mysqli_prepare_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	zval *link = (h->arg & MY_THIS) ? ol_this(ex) : ol_arg(ex, 1);
	if (rv && Z_TYPE_P(rv) == IS_OBJECT) {
		stmt_store(ol_handle_key(rv), ol_arg(ex, MY_BASE(h->arg)), ol_handle_key(link));
	}
}

/* mysqli_stmt_init($link) / mysqli::stmt_init() */
static void mysqli_stmt_init_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	zval *link = (h->arg & MY_THIS) ? ol_this(ex) : ol_arg(ex, 1);
	if (rv && Z_TYPE_P(rv) == IS_OBJECT) {
		stmt_store(ol_handle_key(rv), NULL, ol_handle_key(link));
	}
}

/* mysqli_stmt_prepare($stmt, $q) / mysqli_stmt::prepare($q) */
static void mysqli_stmt_prepare_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	zval *stmt = (h->arg & MY_THIS) ? ol_this(ex) : ol_arg(ex, 1);
	stmt_store(ol_handle_key(stmt), ol_arg(ex, MY_BASE(h->arg)), 0);
}

/* mysqli_stmt::__construct($link, $query = null) */
static void mysqli_stmt_construct_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	stmt_store(ol_handle_key(ol_this(ex)), ol_arg(ex, 2), ol_handle_key(ol_arg(ex, 1)));
}

/* mysqli_stmt_execute($stmt) / mysqli_stmt::execute() */
static void mysqli_stmt_execute_begin(zend_execute_data *ex, const ol_hook *h)
{
	zval *stmt = (h->arg & MY_THIS) ? ol_this(ex) : ol_arg(ex, 1);
	ol_stmt *s = OLG(stmts) ? zend_hash_index_find_ptr(OLG(stmts), ol_handle_key(stmt)) : NULL;
	uint32_t idx = ol_span_begin(ex, "mysql", OL_KIND_CLIENT);
	ol_node *n = ol_node_at(idx);
	if (n == NULL) {
		return;
	}
	conn_attrs(n, "mysql", s ? conn_get(s->link, false) : NULL);
	if (s && s->sql) {
		ol_db_query_attrs(n, "mysql", s->sql, s->len);
	}
}

/* ---------------- pgsql ---------------- */

static void pg_conninfo(ol_conn *c, const char *s, size_t len)
{
	if (ol_str_starts_ci(s, len, "postgres://") || ol_str_starts_ci(s, len, "postgresql://")) {
		const char *p = strstr(s, "://") + 3, *end = s + len, *at, *hs, *he, *slash;
		slash = memchr(p, '/', (size_t) (end - p));
		he = slash ? slash : end;
		at = NULL;
		for (hs = p; hs < he; hs++) {
			if (*hs == '@') at = hs;
		}
		hs = at ? at + 1 : p;
		{
			const char *colon = memchr(hs, ':', (size_t) (he - hs));
			c->host = ol_strdup(hs, (size_t) ((colon ? colon : he) - hs), 256);
			c->port = colon ? ZEND_STRTOL(colon + 1, NULL, 10) : 5432;
		}
		if (slash) {
			const char *q = memchr(slash, '?', (size_t) (end - slash));
			c->db = ol_strdup(slash + 1, (size_t) ((q ? q : end) - slash - 1), 256);
		}
		return;
	}
	c->port = 5432;
	{
		size_t i = 0;
		while (i < len) {
			size_t ks, ke, vs, ve;
			while (i < len && isspace((unsigned char) s[i])) i++;
			ks = i;
			while (i < len && s[i] != '=' && !isspace((unsigned char) s[i])) i++;
			ke = i;
			while (i < len && isspace((unsigned char) s[i])) i++;
			if (i >= len || s[i] != '=') {
				i++;
				continue;
			}
			i++;
			while (i < len && isspace((unsigned char) s[i])) i++;
			if (i < len && s[i] == '\'') {
				vs = ++i;
				while (i < len && s[i] != '\'') i++;
				ve = i++;
			} else {
				vs = i;
				while (i < len && !isspace((unsigned char) s[i])) i++;
				ve = i;
			}
			if (ke - ks == 4 && strncmp(s + ks, "host", 4) == 0) c->host = ol_strdup(s + vs, ve - vs, 256);
			else if (ke - ks == 8 && strncmp(s + ks, "hostaddr", 8) == 0 && c->host == NULL) c->host = ol_strdup(s + vs, ve - vs, 256);
			else if (ke - ks == 4 && strncmp(s + ks, "port", 4) == 0) c->port = ZEND_STRTOL(s + vs, NULL, 10);
			else if (ke - ks == 6 && strncmp(s + ks, "dbname", 6) == 0) c->db = ol_strdup(s + vs, ve - vs, 256);
		}
	}
}

/* pg_connect / pg_pconnect($connection_string) */
static void pg_connect_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	zval *cs = ol_arg(ex, 1);
	zend_ulong key = ol_handle_key(rv);
	ol_conn *c;
	if (key == 0) {
		return;
	}
	OLG(pg_last_conn_key) = key;
	OLG(pg_last_conn_key_set) = 1;
	c = conn_get(key, true);
	if (c && cs && Z_TYPE_P(cs) == IS_STRING) {
		pg_conninfo(c, Z_STRVAL_P(cs), Z_STRLEN_P(cs));
	}
}

static zend_ulong pg_conn_key(zval *conn)
{
	if (conn) {
		return ol_handle_key(conn);
	}
	return OLG(pg_last_conn_key_set) ? OLG(pg_last_conn_key) : 0;
}

static void pg_span(zend_execute_data *ex, zend_ulong key, const char *sql, size_t len)
{
	uint32_t idx = ol_span_begin(ex, "postgresql", OL_KIND_CLIENT);
	ol_node *n = ol_node_at(idx);
	if (n == NULL) {
		return;
	}
	conn_attrs(n, "postgresql", conn_get(key, false));
	if (sql) {
		ol_db_query_attrs(n, "postgresql", sql, len);
	}
}

/* pg_query([$conn,] $sql) (arg 1), pg_query_params([$conn,] $sql, $params) (arg 2) */
static void pg_query_begin(zend_execute_data *ex, const ol_hook *h)
{
	uint32_t nargs = ZEND_CALL_NUM_ARGS(ex);
	bool with_conn = nargs > (uint32_t) h->arg;
	zval *sql = ol_arg(ex, with_conn ? 2 : 1);
	pg_span(ex, pg_conn_key(with_conn ? ol_arg(ex, 1) : NULL),
		sql && Z_TYPE_P(sql) == IS_STRING ? Z_STRVAL_P(sql) : NULL, sql && Z_TYPE_P(sql) == IS_STRING ? Z_STRLEN_P(sql) : 0);
}

static void pg_stmt_key(char *buf, size_t cap, zend_ulong conn, zval *name, size_t *len)
{
	int n = snprintf(buf, cap, "pg:%lu:%s", (unsigned long) conn, name && Z_TYPE_P(name) == IS_STRING ? Z_STRVAL_P(name) : "");
	*len = n > 0 && (size_t) n < cap ? (size_t) n : cap - 1;
}

/* pg_prepare([$conn,] $name, $sql) */
static void pg_prepare_begin(zend_execute_data *ex, const ol_hook *h)
{
	uint32_t nargs = ZEND_CALL_NUM_ARGS(ex);
	bool with_conn = nargs >= 3;
	zval *name = ol_arg(ex, with_conn ? 2 : 1), *sql = ol_arg(ex, with_conn ? 3 : 2);
	char key[300];
	size_t klen;
	const char *copy;
	if (sql == NULL || Z_TYPE_P(sql) != IS_STRING) {
		return;
	}
	pg_stmt_key(key, sizeof(key), pg_conn_key(with_conn ? ol_arg(ex, 1) : NULL), name, &klen);
	copy = ol_strdup(Z_STRVAL_P(sql), Z_STRLEN_P(sql), OL_STR_MAX);
	if (copy) {
		zend_hash_str_update_ptr(ol_table(&OLG(stmts)), key, klen, (void *) copy);
	}
}

/* pg_execute([$conn,] $name, $params) */
static void pg_execute_begin(zend_execute_data *ex, const ol_hook *h)
{
	uint32_t nargs = ZEND_CALL_NUM_ARGS(ex);
	bool with_conn = nargs >= 3;
	zend_ulong conn = pg_conn_key(with_conn ? ol_arg(ex, 1) : NULL);
	char key[300];
	size_t klen;
	const char *sql;
	pg_stmt_key(key, sizeof(key), conn, ol_arg(ex, with_conn ? 2 : 1), &klen);
	sql = OLG(stmts) ? zend_hash_str_find_ptr(OLG(stmts), key, klen) : NULL;
	pg_span(ex, conn, sql, sql ? strlen(sql) : 0);
}

/* ---------------- phpredis ---------------- */

static void redis_connect_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	zval *self = ol_this(ex), *host = ol_arg(ex, 1), *port = ol_arg(ex, 2);
	ol_conn *c = conn_get(ol_handle_key(self), true);
	if (c == NULL) {
		return;
	}
	c->host = zstr_copy(host, 256);
	if (c->host && (strncmp(c->host, "tcp://", 6) == 0 || strncmp(c->host, "tls://", 6) == 0)) {
		c->host += 6;
	}
	c->port = port && Z_TYPE_P(port) == IS_LONG && Z_LVAL_P(port) > 0 ? Z_LVAL_P(port) : (c->host && c->host[0] == '/' ? 0 : 6379);
}

static void redis_select_end(zend_execute_data *ex, zval *rv, const ol_hook *h);

static void redis_span(zend_execute_data *ex, const char *op, size_t oplen, ol_conn *c, uint32_t nargs, zval *args)
{
	uint32_t idx = ol_span_begin(ex, "redis", OL_KIND_CLIENT);
	ol_node *n = ol_node_at(idx);
	char text[OL_STR_MAX + 1];
	size_t tl, i;
	if (n == NULL) {
		return;
	}
	n->name = ol_strdup(op, oplen, 64);
	conn_attrs(n, "redis", c);
	ol_attr_str(n, "db.operation.name", op, oplen);
	if (OLG(query_mode) == OL_QUERY_OFF) {
		return;
	}
	tl = oplen < 64 ? oplen : 64;
	memcpy(text, op, tl);
	for (i = 0; i < nargs && i < 32 && tl + 8 < sizeof(text); i++) {
		zval *a = args ? &args[i] : NULL;
		text[tl++] = ' ';
		if (OLG(query_mode) == OL_QUERY_RAW && a) {
			ZVAL_DEREF(a);
			if (Z_TYPE_P(a) == IS_STRING) {
				size_t l = Z_STRLEN_P(a);
				if (l > 128) l = 128;
				if (tl + l >= sizeof(text) - 1) break;
				memcpy(text + tl, Z_STRVAL_P(a), l);
				tl += l;
				continue;
			} else if (Z_TYPE_P(a) == IS_LONG) {
				tl += (size_t) snprintf(text + tl, sizeof(text) - tl, ZEND_LONG_FMT, Z_LVAL_P(a));
				continue;
			}
		}
		text[tl++] = '?';
	}
	ol_attr_str(n, "db.query.text", text, tl);
}

/* Redis::<command>(...) */
static void redis_cmd_begin(zend_execute_data *ex, const ol_hook *h)
{
	zend_string *fn = ex->func->common.function_name;
	char op[64];
	size_t n = ZSTR_LEN(fn) < sizeof(op) ? ZSTR_LEN(fn) : sizeof(op) - 1, i;
	uint32_t nargs = ZEND_CALL_NUM_ARGS(ex);
	for (i = 0; i < n; i++) {
		op[i] = (char) toupper((unsigned char) ZSTR_VAL(fn)[i]);
	}
	redis_span(ex, op, n, conn_get(ol_handle_key(ol_this(ex)), false), nargs, nargs ? ZEND_CALL_ARG(ex, 1) : NULL);
}

static void redis_cmd_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	db_error_end(ex, rv, false); /* false is a normal reply (e.g. GET of a missing key) */
}

static void redis_select_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	zval *db = ol_arg(ex, 1);
	ol_conn *c;
	redis_cmd_end(ex, rv, h);
	c = conn_get(ol_handle_key(ol_this(ex)), true);
	if (c && db && Z_TYPE_P(db) == IS_LONG && rv && Z_TYPE_P(rv) == IS_TRUE) {
		char buf[24];
		int l = snprintf(buf, sizeof(buf), ZEND_LONG_FMT, Z_LVAL_P(db));
		c->db = ol_strdup(buf, (size_t) l, sizeof(buf));
	}
}

/* ---------------- Predis ---------------- */

/* Predis\Client::executeCommand(CommandInterface $command) */
static void predis_execute_begin(zend_execute_data *ex, const ol_hook *h)
{
	zval *cmd = ol_arg(ex, 1), *self = ol_this(ex), id, *args, *conn, *params, *pp;
	ol_conn c = {0};
	uint32_t nargs = 0;
	char op[64];
	size_t oplen = 0, i;

	if (cmd == NULL || Z_TYPE_P(cmd) != IS_OBJECT || !OL_REC()) {
		return;
	}
	/* getId() only returns a constant: safe to call */
	if (ol_call_method0(Z_OBJ_P(cmd), ZEND_STRL("getid"), &id) && Z_TYPE(id) == IS_STRING) {
		oplen = Z_STRLEN(id) < sizeof(op) ? Z_STRLEN(id) : sizeof(op) - 1;
		for (i = 0; i < oplen; i++) {
			op[i] = (char) toupper((unsigned char) Z_STRVAL(id)[i]);
		}
	}
	zval_ptr_dtor(&id);
	if (oplen == 0) {
		memcpy(op, "COMMAND", 7);
		oplen = 7;
	}
	args = ol_prop(Z_OBJ_P(cmd), ZEND_STRL("arguments"));
	if (args && Z_TYPE_P(args) == IS_ARRAY) {
		nargs = zend_hash_num_elements(Z_ARRVAL_P(args));
	}
	if (self) {
		conn = ol_prop(Z_OBJ_P(self), ZEND_STRL("connection"));
		params = conn && Z_TYPE_P(conn) == IS_OBJECT ? ol_prop(Z_OBJ_P(conn), ZEND_STRL("parameters")) : NULL;
		pp = params && Z_TYPE_P(params) == IS_OBJECT ? ol_prop(Z_OBJ_P(params), ZEND_STRL("parameters")) : NULL;
		if (pp && Z_TYPE_P(pp) == IS_ARRAY) {
			zval *host = ol_array_get(pp, ZEND_STRL("host")), *port = ol_array_get(pp, ZEND_STRL("port"));
			zval *db = ol_array_get(pp, ZEND_STRL("database"));
			c.host = zstr_copy(host, 256);
			c.port = port && Z_TYPE_P(port) == IS_LONG ? Z_LVAL_P(port) : (port && Z_TYPE_P(port) == IS_STRING ? ZEND_STRTOL(Z_STRVAL_P(port), NULL, 10) : 0);
			if (db && (Z_TYPE_P(db) == IS_STRING || Z_TYPE_P(db) == IS_LONG)) {
				char buf[24];
				if (Z_TYPE_P(db) == IS_LONG) {
					snprintf(buf, sizeof(buf), ZEND_LONG_FMT, Z_LVAL_P(db));
					c.db = ol_strdup(buf, strlen(buf), sizeof(buf));
				} else {
					c.db = zstr_copy(db, 64);
				}
			}
		}
	}
	/* arguments are a PHP array: pass the placeholders only (raw values are not read for Predis) */
	redis_span(ex, op, oplen, &c, nargs, NULL);
}

static void predis_execute_end(zend_execute_data *ex, zval *rv, const ol_hook *h)
{
	db_error_end(ex, rv, false);
}

#define MYP(proc, base) ((base))
const ol_hook ol_hooks_datastores[] = {
	/* PDO */
	{"pdo::exec", pdo_dbh_begin, db_client_end, 0, 0},
	{"pdo::query", pdo_dbh_begin, db_client_end, 0, 0},
	{"pdo::prepare", pdo_prepare_begin, pdo_prepare_end, 0, 0},
	{"pdostatement::execute", pdo_stmt_begin, db_client_end, 0, 0},
	/* mysqli: procedural (link = arg 1) and OO (link = $this) */
	{"mysqli_connect", NULL, mysqli_connect_end, MY_RV | 1, OL_HF_ANY},
	{"mysqli::__construct", NULL, mysqli_connect_end, MY_THIS | 1, OL_HF_ANY},
	{"mysqli::connect", NULL, mysqli_connect_end, MY_THIS | 1, OL_HF_ANY},
	{"mysqli::real_connect", NULL, mysqli_connect_end, MY_THIS | 1, OL_HF_ANY},
	{"mysqli_real_connect", NULL, mysqli_connect_end, 2, OL_HF_ANY},
	{"mysqli_select_db", NULL, mysqli_select_db_end, 2, OL_HF_ANY},
	{"mysqli::select_db", NULL, mysqli_select_db_end, MY_THIS | 1, OL_HF_ANY},
	{"mysqli_query", mysqli_query_begin, db_client_end, 2, 0},
	{"mysqli::query", mysqli_query_begin, db_client_end, MY_THIS | 1, 0},
	{"mysqli_real_query", mysqli_query_begin, db_client_end, 2, 0},
	{"mysqli::real_query", mysqli_query_begin, db_client_end, MY_THIS | 1, 0},
	{"mysqli_multi_query", mysqli_query_begin, db_client_end, 2, 0},
	{"mysqli::multi_query", mysqli_query_begin, db_client_end, MY_THIS | 1, 0},
	{"mysqli_execute_query", mysqli_query_begin, db_client_end, 2, 0},
	{"mysqli::execute_query", mysqli_query_begin, db_client_end, MY_THIS | 1, 0},
	{"mysqli_prepare", NULL, mysqli_prepare_end, 2, OL_HF_ANY},
	{"mysqli::prepare", NULL, mysqli_prepare_end, MY_THIS | 1, OL_HF_ANY},
	{"mysqli_stmt_init", NULL, mysqli_stmt_init_end, 1, OL_HF_ANY},
	{"mysqli::stmt_init", NULL, mysqli_stmt_init_end, MY_THIS, OL_HF_ANY},
	{"mysqli_stmt_prepare", NULL, mysqli_stmt_prepare_end, 2, OL_HF_ANY},
	{"mysqli_stmt::prepare", NULL, mysqli_stmt_prepare_end, MY_THIS | 1, OL_HF_ANY},
	{"mysqli_stmt::__construct", NULL, mysqli_stmt_construct_end, 0, OL_HF_ANY},
	{"mysqli_stmt_execute", mysqli_stmt_execute_begin, db_client_end, 1, 0},
	{"mysqli_stmt::execute", mysqli_stmt_execute_begin, db_client_end, MY_THIS, 0},
	/* pgsql */
	{"pg_connect", NULL, pg_connect_end, 0, OL_HF_ANY},
	{"pg_pconnect", NULL, pg_connect_end, 0, OL_HF_ANY},
	{"pg_query", pg_query_begin, db_client_end, 1, 0},
	{"pg_exec", pg_query_begin, db_client_end, 1, 0},
	{"pg_query_params", pg_query_begin, db_client_end, 2, 0},
	{"pg_prepare", pg_prepare_begin, NULL, 0, OL_HF_ANY},
	{"pg_execute", pg_execute_begin, db_client_end, 0, 0},
	/* phpredis */
	{"redis::*", redis_cmd_begin, redis_cmd_end, 0, 0},
	{"redis::connect", NULL, redis_connect_end, 0, OL_HF_ANY},
	{"redis::pconnect", NULL, redis_connect_end, 0, OL_HF_ANY},
	{"redis::open", NULL, redis_connect_end, 0, OL_HF_ANY},
	{"redis::popen", NULL, redis_connect_end, 0, OL_HF_ANY},
	{"redis::select", redis_cmd_begin, redis_select_end, 0, 0},
	{"redis::__construct", NULL, NULL, 0, 0},
	{"redis::__destruct", NULL, NULL, 0, 0},
	{"redis::close", NULL, NULL, 0, 0},
	{"redis::setoption", NULL, NULL, 0, 0},
	{"redis::getoption", NULL, NULL, 0, 0},
	{"redis::isconnected", NULL, NULL, 0, 0},
	{"redis::gethost", NULL, NULL, 0, 0},
	{"redis::getport", NULL, NULL, 0, 0},
	{"redis::getdbnum", NULL, NULL, 0, 0},
	{"redis::gettimeout", NULL, NULL, 0, 0},
	{"redis::getreadtimeout", NULL, NULL, 0, 0},
	{"redis::getpersistentid", NULL, NULL, 0, 0},
	{"redis::getauth", NULL, NULL, 0, 0},
	{"redis::getlasterror", NULL, NULL, 0, 0},
	{"redis::clearlasterror", NULL, NULL, 0, 0},
	{"redis::getmode", NULL, NULL, 0, 0},
	{"redis::_prefix", NULL, NULL, 0, 0},
	{"redis::_serialize", NULL, NULL, 0, 0},
	{"redis::_unserialize", NULL, NULL, 0, 0},
	{"redis::_pack", NULL, NULL, 0, 0},
	{"redis::_unpack", NULL, NULL, 0, 0},
	{"redis::_compress", NULL, NULL, 0, 0},
	{"redis::_uncompress", NULL, NULL, 0, 0},
	/* Predis */
	{"predis\\client::executecommand", predis_execute_begin, predis_execute_end, 0, 0},
	{NULL, NULL, NULL, 0, 0}
};
