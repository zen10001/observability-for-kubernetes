package events

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wavefronthq/observability-for-kubernetes/collector/internal/configuration"
	"github.com/wavefronthq/observability-for-kubernetes/collector/internal/events"
	"github.com/wavefronthq/observability-for-kubernetes/collector/internal/metrics"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
)

func TestAddEvent(t *testing.T) {
	t.Run("forwards the event to the sink", func(t *testing.T) {
		sink := &MockExport{}
		er := NewEventRouter(fake.NewSimpleClientset(), configuration.EventsConfig{}, sink, true, testWorkloadCache{})
		event := fakeEvent()

		er.addEvent(event, false)

		require.Equal(t, event.Message, sink.Message)
		require.Equal(t, event.LastTimestamp, metav1.NewTime(sink.Ts))
		require.Equal(t, event.Source.Host, sink.Host)
		require.Equal(t, "Normal", sink.Annotations["type"])
		require.Equal(t, "some-kind", sink.Annotations["kind"])
		require.Equal(t, "some-reason", sink.Annotations["reason"])
		require.Equal(t, "some-component", sink.Annotations["component"])
		require.Equal(t, "test-name", sink.Annotations["resource_name"])
		require.NotContains(t, sink.Annotations, "pod_name")
		require.Equal(t, "test-namespace", event.InvolvedObject.Namespace)
	})

	t.Run("does not send add events for events that already existed prior to startup", func(t *testing.T) {
		event := fakeEvent()
		client := fake.NewSimpleClientset(event)
		workloadCache := testWorkloadCache{}
		sink := &MockExport{}
		er := NewEventRouter(client, configuration.EventsConfig{}, sink, true, workloadCache)
		go er.Resume()
		defer er.Stop()

		cache.WaitForCacheSync(make(chan struct{}), er.eListerSynced)

		sink.SyncAccess(func() {
			require.Empty(t, sink.Message)
		})
	})

	t.Run("sends events for updates", func(t *testing.T) {
		event := fakeEvent()
		client := fake.NewSimpleClientset(event)
		workloadCache := testWorkloadCache{}
		sink := &MockExport{}
		er := NewEventRouter(client, configuration.EventsConfig{}, sink, true, workloadCache)
		go er.Resume()
		defer er.Stop()
		cache.WaitForCacheSync(make(chan struct{}), er.eListerSynced)

		event.Count += 1
		client.CoreV1().Events(event.Namespace).Update(context.Background(), event, metav1.UpdateOptions{})
		time.Sleep(10 * time.Millisecond)

		sink.SyncAccess(func() {
			require.NotEmpty(t, sink.Message)
		})
	})

	t.Run("does not send events when the update has not changed anything", func(t *testing.T) {
		event := fakeEvent()
		client := fake.NewSimpleClientset(event)
		workloadCache := testWorkloadCache{}
		sink := &MockExport{}
		er := NewEventRouter(client, configuration.EventsConfig{}, sink, true, workloadCache)
		go er.Resume()
		defer er.Stop()
		cache.WaitForCacheSync(make(chan struct{}), er.eListerSynced)

		client.CoreV1().Events(event.Namespace).Update(context.Background(), event, metav1.UpdateOptions{})
		time.Sleep(10 * time.Millisecond)

		sink.SyncAccess(func() {
			require.Empty(t, sink.Message)
		})
	})
}

func TestAddEventHasWorkload(t *testing.T) {
	fakeClient := fake.NewSimpleClientset()
	fakePod := createFakePod(t, fakeClient, nil)
	workloadCache := &testWorkloadCache{
		workloadName: "some-workload-name",
		workloadKind: "some-workload-kind",
		nodeName:     "some-node-name",
	}
	sink := &MockExport{}
	cfg := configuration.EventsConfig{ClusterName: "some-cluster-name", ClusterUUID: "some-cluster-uuid"}
	event := fakeEvent()
	event.InvolvedObject.Kind = fakePod.Kind
	event.InvolvedObject.Namespace = fakePod.Namespace
	event.InvolvedObject.Name = fakePod.Name
	er := NewEventRouter(fakeClient, cfg, sink, true, workloadCache)

	er.addEvent(event, false)

	require.Equal(t, event.Message, sink.Message)
	require.Equal(t, event.LastTimestamp, metav1.NewTime(sink.Ts))
	require.Equal(t, event.Source.Host, sink.Host)
	require.Equal(t, "Normal", sink.Annotations["type"])
	require.Equal(t, fakePod.Kind, sink.Annotations["kind"])
	require.Equal(t, "some-reason", sink.Annotations["reason"])
	require.Equal(t, "some-component", sink.Annotations["component"])
	require.Equal(t, fakePod.Name, sink.Annotations["pod_name"])
	require.NotContains(t, sink.Annotations, "resource_name")
	require.Equal(t, fakePod.Namespace, sink.InvolvedObject.Namespace)
	require.Equal(t, "some-cluster-name", sink.ObjectMeta.Annotations["aria/cluster-name"])
	require.Equal(t, "some-cluster-uuid", sink.ObjectMeta.Annotations["aria/cluster-uuid"])
	require.Equal(t, "some-workload-kind", sink.ObjectMeta.Annotations["aria/workload-kind"])
	require.Equal(t, "some-workload-name", sink.ObjectMeta.Annotations["aria/workload-name"])
	require.Equal(t, "some-node-name", sink.ObjectMeta.Annotations["aria/node-name"])
}

func TestEmptyNodeNameExcludesAnnotation(t *testing.T) {
	fakeClient := fake.NewSimpleClientset()
	sink := &MockExport{}
	workloadCache := &testWorkloadCache{
		workloadName: "some-workload-name",
		workloadKind: "some-workload-kind",
	}
	cfg := configuration.EventsConfig{ClusterName: "some-cluster-name", ClusterUUID: "some-cluster-uuid"}
	event := fakeEvent()
	er := NewEventRouter(fakeClient, cfg, sink, true, workloadCache)

	er.addEvent(event, false)

	require.NotContains(t, event.ObjectMeta.Annotations, "aria/node-name")
}

func fakeEvent() *v1.Event {
	return &v1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "test-namespace",
		},
		Message:       "Test message for events",
		LastTimestamp: metav1.NewTime(time.Now()),
		Source: v1.EventSource{
			Host:      "test Host",
			Component: "some-component",
		},
		InvolvedObject: v1.ObjectReference{
			Namespace: "test-namespace",
			Kind:      "some-kind",
			Name:      "test-name",
		},
		Type:   "Normal",
		Reason: "some-reason",
	}
}

type testWorkloadCache struct {
	workloadName string
	workloadKind string
	nodeName     string
}

func (wc testWorkloadCache) GetWorkloadForPodName(podName, ns string) (name, kind, nodeName string) {
	return wc.workloadName, wc.workloadKind, wc.nodeName
}
func (wc testWorkloadCache) GetWorkloadForPod(pod *v1.Pod) (string, string) {
	return wc.workloadName, wc.workloadKind
}

type MockExport struct {
	mu sync.Mutex
	events.Event
	Annotations map[string]string
}

func (m *MockExport) SyncAccess(do func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	do()
}

func (m *MockExport) ExportEvent(event *events.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tagMap := map[string]interface{}{
		"annotations": map[string]string{},
	}
	for _, e := range event.Options {
		e(tagMap)
	}
	m.Message = event.Message
	m.Ts = event.Ts
	m.Host = event.Host
	m.Annotations = tagMap["annotations"].(map[string]string)
	m.Event = *event
}
func (m *MockExport) Export(batch *metrics.Batch) {}
func (m *MockExport) Name() string                { return "" }
func (m *MockExport) Stop()                       {}

func createFakePod(t *testing.T, fakeClient *fake.Clientset, owner *metav1.OwnerReference) *v1.Pod {
	podSpec := &v1.Pod{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Pod",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "a-pod",
			Namespace: "a-ns",
		},
	}
	if owner != nil {
		podSpec.OwnerReferences = []metav1.OwnerReference{*owner}
	}

	podsClient := fakeClient.CoreV1().Pods("a-ns")
	pod, err := podsClient.Create(context.Background(), podSpec, metav1.CreateOptions{})
	require.NoError(t, err)
	return pod
}
