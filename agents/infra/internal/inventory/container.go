package inventory

import "github.com/onuragtas/openlog/agents/infra/internal/containers"

// Container is the body of a "container" item (Docker Engine API).
type Container = containers.Container

// CategoryContainer is the container inventory category.
const CategoryContainer = "container"
