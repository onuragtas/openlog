--TEST--
Predis (stubbed classes): executeCommand as a redis client span with connection parameters
--SKIPIF--
<?php require __DIR__ . '/inc/skipif.php'; ?>
--FILE--
<?php
require __DIR__ . '/inc/harness.php';
$r = ol_run('<?php
namespace Predis\Command {
    interface CommandInterface {}
    abstract class Command implements CommandInterface {
        private $arguments;
        public function __construct(array $args) { $this->arguments = $args; }
        abstract public function getId();
    }
    class StringGet extends Command { public function getId() { return "get"; } }
    class Broken extends Command { public function getId() { throw new \RuntimeException("no id"); } }
}
namespace Predis\Connection {
    class Parameters { protected $parameters = ["host" => "cache.local", "port" => 6380, "database" => 2]; }
    class StreamConnection { protected $parameters; public function __construct() { $this->parameters = new Parameters(); } }
}
namespace Predis {
    class Client {
        private $connection;
        public function __construct() { $this->connection = new Connection\StreamConnection(); }
        public function executeCommand(Command\CommandInterface $command) { return "value"; }
        public function __call($method, $args) { return $this->executeCommand(new Command\StringGet($args)); }
    }
}
namespace {
    $c = new Predis\Client();
    echo $c->get("key"), "\n";
    echo $c->executeCommand(new Predis\Command\Broken([])), "\n";
}
');
echo $r['out'];
ol_print_tree(ol_one_trace($r), ['db.system.name', 'server.address', 'server.port', 'db.namespace', 'db.query.text'], false);
--EXPECTF--
value
value
php %s kind=1 status=0
  GET kind=3 status=0 db.system.name="redis" server.address="cache.local" server.port=6380 db.namespace="2" db.query.text="GET ?"
  COMMAND kind=3 status=0 db.system.name="redis" server.address="cache.local" server.port=6380 db.namespace="2" db.query.text="COMMAND"
