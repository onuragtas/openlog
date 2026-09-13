package mask

import "testing"

func TestCmdline(t *testing.T) {
	cases := []struct{ name, exe, in, want string }{
		{"empty", "", "", ""},
		{"plain", "/usr/sbin/nginx", "/usr/sbin/nginx -g daemon off;", "/usr/sbin/nginx -g daemon off;"},
		{"password eq", "", "app --password=hunter2 --verbose", "app --password=*** --verbose"},
		{"password space", "", "app --password hunter2 --verbose", "app --password *** --verbose"},
		{"single dash", "", "app -password hunter2", "app -password ***"},
		{"passwd", "", "tool --passwd=x", "tool --passwd=***"},
		{"pwd", "", "tool --pwd x", "tool --pwd ***"},
		{"secret", "", "tool --secret=abc", "tool --secret=***"},
		{"token upper", "", "tool --TOKEN=abc", "tool --TOKEN=***"},
		{"api-key", "", "tool --api-key=abc", "tool --api-key=***"},
		{"api_key", "", "tool --api_key abc", "tool --api_key ***"},
		{"apikey", "", "tool --apikey=abc", "tool --apikey=***"},
		{"auth", "", "tool --auth user:pw", "tool --auth ***"},
		{"credential", "", "tool --credential=a", "tool --credential=***"},
		{"credentials", "", "tool --credentials a", "tool --credentials ***"},
		{"compound flag", "", "tool --auth-token=abc", "tool --auth-token=***"},

		// -p<value> only for MySQL/MariaDB clients (by exe or argv[0] basename).
		{"mysql -p", "/usr/bin/mysql", "mysql -uroot -psecret db", "mysql -uroot -p*** db"},
		{"mysql -p by argv0", "", "/usr/bin/mysql -psecret", "/usr/bin/mysql -p***"},
		{"mariadb -p", "/usr/bin/mariadb", "mariadb -psecret", "mariadb -p***"},
		{"mariadb-dump -p", "", "mariadb-dump -psecret --all-databases", "mariadb-dump -p*** --all-databases"},
		{"mysqldump -p", "/usr/bin/mysqldump", "mysqldump -uroot -pS3cr3t app", "mysqldump -uroot -p*** app"},
		{"mysql symlinked to mariadb", "/usr/bin/mariadb", "mysql -psecret", "mysql -p***"},
		{"bare -p kept", "/usr/bin/mysql", "mysql -u root -p db", "mysql -u root -p db"},
		{"ssh -p22 kept", "/usr/bin/ssh", "ssh -p22 host", "ssh -p22 host"},
		{"docker -p kept", "/usr/bin/docker", "docker run -p 8080:80 nginx", "docker run -p 8080:80 nginx"},
		{"docker -p attached kept", "/usr/bin/docker", "docker run -p8080:80 nginx", "docker run -p8080:80 nginx"},
		{"redis-cli -p kept", "", "redis-cli -p6380 ping", "redis-cli -p6380 ping"},

		// KEY=value
		{"env token", "", "run GITHUB_TOKEN=ghp_abc other=1", "run GITHUB_TOKEN=*** other=1"},
		{"env pass", "", "DB_PASSWORD=x ./app", "DB_PASSWORD=*** ./app"},
		{"env key lower", "", "api_key=zzz", "api_key=***"},
		{"env auth", "", "X_AUTH=zzz", "X_AUTH=***"},
		{"non secret kv", "", "LANG=C mode=fast", "LANG=C mode=fast"},
		{"keyfile kept", "", "nginx --keyfile=/etc/x.pem", "nginx --keyfile=/etc/x.pem"},
		{"key path kept", "", "app --ssl-key-path=/etc/tls/key.pem TOKEN_DIR=/run/tokens PASSWORD_FILE=/run/secrets/pw", "app --ssl-key-path=/etc/tls/key.pem TOKEN_DIR=/run/tokens PASSWORD_FILE=/run/secrets/pw"},
		{"java prop", "", "java -Djavax.net.ssl.keyStorePassword=changeit -jar a.jar", "java -Djavax.net.ssl.keyStorePassword=*** -jar a.jar"},

		// URL userinfo
		{"url userinfo", "", "app --dsn postgres://bob:s3cr3t@db:5432/x", "app --dsn postgres://bob:***@db:5432/x"},
		{"url userinfo amqp", "", "worker amqp://guest:guest@localhost/", "worker amqp://guest:***@localhost/"},
		{"url no password", "", "curl https://example.com/a:b", "curl https://example.com/a:b"},
		{"url user only", "", "git clone https://bob@host/repo", "git clone https://bob@host/repo"},
		{"multiple", "", "a --token t1 -pX PASS=y redis://:pw@h", "a --token *** -pX PASS=*** redis://:***@h"},
		{"multiple mysql", "/usr/bin/mysql", "mysql --token t1 -pX PASS=y redis://:pw@h", "mysql --token *** -p*** PASS=*** redis://:***@h"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Cmdline(c.exe, c.in); got != c.want {
				t.Errorf("Cmdline(%q, %q)\n got  %q\n want %q", c.exe, c.in, got, c.want)
			}
		})
	}
}

func TestCmdlineIdempotent(t *testing.T) {
	in := "mysql --password=x -pY TOKEN=z http://u:p@h"
	once := Cmdline("/usr/bin/mysql", in)
	if twice := Cmdline("/usr/bin/mysql", once); twice != once {
		t.Errorf("not idempotent: %q -> %q", once, twice)
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("héllo", 2); got != "h" {
		t.Errorf("got %q", got)
	}
	if got := Truncate("abc", 5); got != "abc" {
		t.Errorf("got %q", got)
	}
	if got := Truncate("abcdef", 3); got != "abc" {
		t.Errorf("got %q", got)
	}
}

func TestText(t *testing.T) {
	in := "login user=bob password=hunter2 --token abc url=https://u:p4ss@host/x api_key=zzz keyfile=/etc/k.pem -psecret"
	want := "login user=bob password=*** --token *** url=https://u:***@host/x api_key=*** keyfile=/etc/k.pem -psecret"
	if got := Text(in); got != want {
		t.Errorf("Text:\n got  %s\n want %s", got, want)
	}
}
