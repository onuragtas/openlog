package agent

import (
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/config"
	"github.com/onuragtas/openlog/agents/infra/internal/containers"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/apache"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/docker"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/elasticsearch"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/haproxy"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/iis"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/jvm"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/kafka"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/memcached"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/mongodb"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/mssql"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/mysql"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/nginx"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/postgresql"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/rabbitmq"
	"github.com/onuragtas/openlog/agents/infra/internal/integrations/redis"
)

// statusSnapshotGap is the minimum time between inventory snapshots triggered
// only by integration status changes (first results after start are exempt).
const statusSnapshotGap = time.Minute

// Registry returns the integrations implemented by this agent.
func Registry(cfg *config.Config, ctr *containers.Source) []integrations.Integration {
	return []integrations.Integration{
		nginx.Integration{},
		redis.Integration{},
		mysql.Integration{},
		postgresql.Integration{},
		docker.Integration{Source: ctr, Socket: cfg.Containers.DockerSocket},
		mssql.Integration{},
		iis.Integration{}, // Windows performance counters; not_available elsewhere
		apache.Integration{},
		memcached.Integration{},
		haproxy.Integration{},
		rabbitmq.Integration{},
		elasticsearch.Integration{},
		mongodb.Integration{},
		jvm.Integration{},
		kafka.Integration{},
	}
}
