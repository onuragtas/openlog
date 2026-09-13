dnl openlog PHP agent extension (openlog.so) - SPDX-License-Identifier: Apache-2.0
PHP_ARG_ENABLE([openlog],
  [whether to enable the openlog APM agent],
  [AS_HELP_STRING([--enable-openlog], [Enable the openlog APM agent extension])],
  [yes])

if test "$PHP_OPENLOG" != "no"; then
  OPENLOG_SOURCES="src/ol_module.c src/ol_core.c src/ol_context.c src/ol_json.c src/ol_hooks.c src/ol_util.c \
    src/ol_sampler.c src/inst_frameworks.c src/inst_datastores.c src/inst_http.c"
  dnl The transaction tracer's sampler thread.
  PHP_ADD_LIBRARY(pthread, 1, OPENLOG_SHARED_LIBADD)
  PHP_SUBST(OPENLOG_SHARED_LIBADD)
  PHP_NEW_EXTENSION(openlog, $OPENLOG_SOURCES, $ext_shared,, -DZEND_ENABLE_STATIC_TSRMLS_CACHE=1 -Wall -Wno-unused-parameter -Wno-missing-field-initializers -fvisibility=hidden)
  PHP_ADD_BUILD_DIR([$ext_builddir/src])
  PHP_ADD_INCLUDE([$ext_srcdir/src])
  dnl Optional: load after these so their classes/functions exist when the hooks are resolved.
  PHP_ADD_EXTENSION_DEP(openlog, pdo, true)
  PHP_ADD_EXTENSION_DEP(openlog, curl, true)
  PHP_ADD_EXTENSION_DEP(openlog, mysqli, true)
  PHP_ADD_EXTENSION_DEP(openlog, pgsql, true)
  PHP_ADD_EXTENSION_DEP(openlog, redis, true)
fi
