package k8s

import (
	"encoding/json"
	"strings"
	"time"
)

// Minimal typed views of the Kubernetes objects the agent reads. Unknown fields are ignored by encoding/json.

// ObjectMeta is the part of metav1.ObjectMeta used by the agent.
type ObjectMeta struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	UID               string            `json:"uid"`
	ResourceVersion   string            `json:"resourceVersion"`
	CreationTimestamp *time.Time        `json:"creationTimestamp"`
	DeletionTimestamp *time.Time        `json:"deletionTimestamp"`
	Labels            map[string]string `json:"labels"`
	Annotations       map[string]string `json:"annotations"`
	OwnerReferences   []OwnerReference  `json:"ownerReferences"`
}

// OwnerReference is metav1.OwnerReference.
type OwnerReference struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
	Controller *bool  `json:"controller"`
}

// ListMeta is metav1.ListMeta.
type ListMeta struct {
	ResourceVersion string `json:"resourceVersion"`
	Continue        string `json:"continue"`
}

// controllerOf returns the controller owner reference (or the first owner when none is marked).
func controllerOf(m ObjectMeta) *OwnerReference {
	for i := range m.OwnerReferences {
		if c := m.OwnerReferences[i].Controller; c != nil && *c {
			return &m.OwnerReferences[i]
		}
	}
	if len(m.OwnerReferences) > 0 {
		return &m.OwnerReferences[0]
	}
	return nil
}

// ResourceList maps resource names to quantities.
type ResourceList map[string]string

// Container is corev1.Container (resources only).
type Container struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	Ports []struct {
		ContainerPort int    `json:"containerPort"`
		Protocol      string `json:"protocol"`
	} `json:"ports"`
	Resources struct {
		Requests ResourceList `json:"requests"`
		Limits   ResourceList `json:"limits"`
	} `json:"resources"`
}

// ContainerState is corev1.ContainerState.
type ContainerState struct {
	Waiting *struct {
		Reason string `json:"reason"`
	} `json:"waiting"`
	Running *struct {
		StartedAt *time.Time `json:"startedAt"`
	} `json:"running"`
	Terminated *struct {
		Reason   string `json:"reason"`
		ExitCode int    `json:"exitCode"`
	} `json:"terminated"`
}

// ContainerStatus is corev1.ContainerStatus.
type ContainerStatus struct {
	Name         string         `json:"name"`
	ContainerID  string         `json:"containerID"`
	Image        string         `json:"image"`
	Ready        bool           `json:"ready"`
	RestartCount int            `json:"restartCount"`
	State        ContainerState `json:"state"`
	LastState    ContainerState `json:"lastState"`
}

// Condition is a status condition.
type Condition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

// Pod is corev1.Pod.
type Pod struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     struct {
		NodeName   string      `json:"nodeName"`
		Containers []Container `json:"containers"`
	} `json:"spec"`
	Status struct {
		Phase             string            `json:"phase"`
		Reason            string            `json:"reason"`
		PodIP             string            `json:"podIP"`
		QOSClass          string            `json:"qosClass"`
		StartTime         *time.Time        `json:"startTime"`
		Conditions        []Condition       `json:"conditions"`
		ContainerStatuses []ContainerStatus `json:"containerStatuses"`
	} `json:"status"`
}

// PodList is corev1.PodList.
type PodList struct {
	Metadata ListMeta `json:"metadata"`
	Items    []Pod    `json:"items"`
}

// Node is corev1.Node.
type Node struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     struct {
		Unschedulable bool `json:"unschedulable"`
	} `json:"spec"`
	Status struct {
		Allocatable ResourceList `json:"allocatable"`
		Conditions  []Condition  `json:"conditions"`
		Addresses   []struct {
			Type    string `json:"type"`
			Address string `json:"address"`
		} `json:"addresses"`
		NodeInfo struct {
			KubeletVersion          string `json:"kubeletVersion"`
			OSImage                 string `json:"osImage"`
			ContainerRuntimeVersion string `json:"containerRuntimeVersion"`
		} `json:"nodeInfo"`
	} `json:"status"`
}

// NodeList is corev1.NodeList.
type NodeList struct {
	Items []Node `json:"items"`
}

// Deployment is appsv1.Deployment.
type Deployment struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     struct {
		Replicas *int `json:"replicas"`
	} `json:"spec"`
	Status struct {
		ReadyReplicas     int `json:"readyReplicas"`
		AvailableReplicas int `json:"availableReplicas"`
		UpdatedReplicas   int `json:"updatedReplicas"`
	} `json:"status"`
}

// StatefulSet is appsv1.StatefulSet.
type StatefulSet struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     struct {
		Replicas *int `json:"replicas"`
	} `json:"spec"`
	Status struct {
		Replicas          int `json:"replicas"`
		ReadyReplicas     int `json:"readyReplicas"`
		CurrentReplicas   int `json:"currentReplicas"`
		UpdatedReplicas   int `json:"updatedReplicas"`
		AvailableReplicas int `json:"availableReplicas"`
	} `json:"status"`
}

// DaemonSet is appsv1.DaemonSet.
type DaemonSet struct {
	Metadata ObjectMeta `json:"metadata"`
	Status   struct {
		DesiredNumberScheduled int `json:"desiredNumberScheduled"`
		CurrentNumberScheduled int `json:"currentNumberScheduled"`
		NumberMisscheduled     int `json:"numberMisscheduled"`
		NumberReady            int `json:"numberReady"`
		NumberAvailable        int `json:"numberAvailable"`
		UpdatedNumberScheduled int `json:"updatedNumberScheduled"`
	} `json:"status"`
}

// ReplicaSet is appsv1.ReplicaSet.
type ReplicaSet struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     struct {
		Replicas *int `json:"replicas"`
	} `json:"spec"`
	Status struct {
		ReadyReplicas     int `json:"readyReplicas"`
		AvailableReplicas int `json:"availableReplicas"`
	} `json:"status"`
}

// Job is batchv1.Job.
type Job struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     struct {
		Completions *int `json:"completions"`
		Parallelism *int `json:"parallelism"`
	} `json:"spec"`
	Status struct {
		Active    int  `json:"active"`
		Succeeded int  `json:"succeeded"`
		Failed    int  `json:"failed"`
		Ready     *int `json:"ready"`
	} `json:"status"`
}

// CronJob is batchv1.CronJob.
type CronJob struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     struct {
		Suspend *bool `json:"suspend"`
	} `json:"spec"`
	Status struct {
		Active []struct {
			Name string `json:"name"`
		} `json:"active"`
	} `json:"status"`
}

// HPA is autoscalingv2.HorizontalPodAutoscaler.
type HPA struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     struct {
		MinReplicas    *int `json:"minReplicas"`
		MaxReplicas    int  `json:"maxReplicas"`
		ScaleTargetRef struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"scaleTargetRef"`
	} `json:"spec"`
	Status struct {
		CurrentReplicas int `json:"currentReplicas"`
		DesiredReplicas int `json:"desiredReplicas"`
	} `json:"status"`
}

// Namespace is corev1.Namespace.
type Namespace struct {
	Metadata ObjectMeta `json:"metadata"`
	Status   struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

// ResourceQuota is corev1.ResourceQuota.
type ResourceQuota struct {
	Metadata ObjectMeta `json:"metadata"`
	Status   struct {
		Hard ResourceList `json:"hard"`
		Used ResourceList `json:"used"`
	} `json:"status"`
}

// List is a generic list of items.
type List[T any] struct {
	Metadata ListMeta `json:"metadata"`
	Items    []T      `json:"items"`
}

// MicroTime is metav1.MicroTime (RFC3339 with microseconds).
type MicroTime struct{ time.Time }

// MicroTimeFormat is the wire format of MicroTime.
const MicroTimeFormat = "2006-01-02T15:04:05.000000Z07:00"

// MarshalJSON implements json.Marshaler.
func (t MicroTime) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(t.UTC().Format(MicroTimeFormat))
}

// UnmarshalJSON implements json.Unmarshaler.
func (t *MicroTime) UnmarshalJSON(b []byte) error {
	var s *string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s == nil || *s == "" {
		t.Time = time.Time{}
		return nil
	}
	v, err := time.Parse(time.RFC3339Nano, *s)
	if err != nil {
		return err
	}
	t.Time = v
	return nil
}

// Event is corev1.Event.
type Event struct {
	Metadata       ObjectMeta `json:"metadata"`
	InvolvedObject struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		UID       string `json:"uid"`
		FieldPath string `json:"fieldPath"`
	} `json:"involvedObject"`
	Reason         string     `json:"reason"`
	Message        string     `json:"message"`
	Type           string     `json:"type"`
	Count          int        `json:"count"`
	Action         string     `json:"action"`
	FirstTimestamp *time.Time `json:"firstTimestamp"`
	LastTimestamp  *time.Time `json:"lastTimestamp"`
	EventTime      MicroTime  `json:"eventTime"`
	Source         struct {
		Component string `json:"component"`
		Host      string `json:"host"`
	} `json:"source"`
	ReportingController string `json:"reportingComponent"`
	Series              *struct {
		Count            int       `json:"count"`
		LastObservedTime MicroTime `json:"lastObservedTime"`
	} `json:"series"`
}

// Lease is coordinationv1.Lease.
type Lease struct {
	APIVersion string     `json:"apiVersion,omitempty"`
	Kind       string     `json:"kind,omitempty"`
	Metadata   LeaseMeta  `json:"metadata"`
	Spec       LeaseSpecs `json:"spec"`
}

// LeaseMeta is the metadata written with a Lease.
type LeaseMeta struct {
	Name            string `json:"name"`
	Namespace       string `json:"namespace,omitempty"`
	ResourceVersion string `json:"resourceVersion,omitempty"`
}

// LeaseSpecs is coordinationv1.LeaseSpec.
type LeaseSpecs struct {
	HolderIdentity       *string    `json:"holderIdentity,omitempty"`
	LeaseDurationSeconds *int       `json:"leaseDurationSeconds,omitempty"`
	AcquireTime          *MicroTime `json:"acquireTime,omitempty"`
	RenewTime            *MicroTime `json:"renewTime,omitempty"`
	LeaseTransitions     *int       `json:"leaseTransitions,omitempty"`
}

// trimRuntimePrefix removes the "<runtime>://" prefix of a container id.
func trimRuntimePrefix(id string) string {
	if i := strings.Index(id, "://"); i >= 0 {
		return strings.ToLower(id[i+3:])
	}
	return strings.ToLower(id)
}

func formatTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
