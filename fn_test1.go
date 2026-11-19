package main

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	fnv1 "github.com/crossplane/function-sdk-go/proto/v1"
	"github.com/crossplane/function-sdk-go/resource"
)

// rolloutRequest is a request fixture. It holds an observed
// WebApp with the supplied spec. If deploymentStatus is not
// empty, it also holds an observed Deployment with that status.
func rolloutRequest(spec,
	deploymentStatus string) *fnv1.RunFunctionRequest {
	req := &fnv1.RunFunctionRequest{
		Observed: &fnv1.State{
			Composite: &fnv1.Resource{
				Resource: resource.MustStructJSON(`{
					"apiVersion": "platform.example.com/v1alpha1",
					"kind": "WebApp",
					"metadata": {"name": "catalog", "namespace": "team-catalog"},
					"spec": ` + spec + `
				}`),
			},
		},
	}
	if deploymentStatus != "" {
		req.Observed.Resources = map[string]*fnv1.Resource{
			"deployment": {
				Resource: resource.MustStructJSON(`{
					"apiVersion": "apps/v1",
					"kind": "Deployment",
					"metadata": {"name": "catalog-x7k2p"},
					"status": ` + deploymentStatus + `
				}`),
			},
		}
	}
	return req
}

// hasResult reports whether the response holds a result with
// the given severity whose message contains text.
func hasResult(rsp *fnv1.RunFunctionResponse,
	sev fnv1.Severity, text string) bool {
	for _, r := range rsp.GetResults() {
		if r.GetSeverity() == sev &&
			strings.Contains(r.GetMessage(), text) {
			return true
		}
	}
	return false
}

// xrStatus returns one field of the desired XR status.
func xrStatus(rsp *fnv1.RunFunctionResponse,
	field string) *structpb.Value {
	xr := rsp.GetDesired().GetComposite().GetResource()
	status := xr.GetFields()["status"].GetStructValue()
	return status.GetFields()[field]
}

func TestRunFunction(t *testing.T) {
	spec := `{"image": "nginx:1.27", "replicas": 3}`

	cases := map[string]struct {
		reason        string
		req           *fnv1.RunFunctionRequest
		wantFatal     string    // text of the expected fatal result
		wantReady     fnv1.Ready // readiness of the Deployment
		wantRollout   string    // expected status.rolloutStatus
		wantAvailable float64   // expected status.availableReplicas
		wantNormal    string    // text of an expected normal result
	}{
		"FirstReconcileNoObservedDeployment": {
			reason: "Nothing exists yet, so the function " +
				"creates the Deployment and waits.",
			req:           rolloutRequest(spec, ""),
			wantReady:     fnv1.Ready_READY_FALSE,
			wantRollout:   "Deployment is being created",
			wantAvailable: 0,
			wantNormal:    "first reconcile",
		},
		"RolloutInProgress": {
			reason: "1 of 3 replicas available keeps the " +
				"WebApp not ready.",
			req: rolloutRequest(spec,
				`{"availableReplicas": 1}`),
			wantReady:     fnv1.Ready_READY_FALSE,
			wantRollout:   "Waiting: 1 of 3 replicas available",
			wantAvailable: 1,
		},
		"AvailableFieldMissing": {
			reason: "Kubernetes omits availableReplicas while " +
				"it is 0; treat that as 0.",
			req:           rolloutRequest(spec, `{}`),
			wantReady:     fnv1.Ready_READY_FALSE,
			wantRollout:   "Waiting: 0 of 3 replicas available",
			wantAvailable: 0,
		},
		"RolloutComplete": {
			reason: "3 of 3 replicas available makes the " +
				"WebApp ready.",
			req: rolloutRequest(spec,
				`{"availableReplicas": 3}`),
			wantReady:     fnv1.Ready_READY_TRUE,
			wantRollout:   "Available",
			wantAvailable: 3,
		},
		"MoreThanTargetIsReady": {
			reason: "During a scale-down more replicas than the " +
				"target can be available (boundary).",
			req: rolloutRequest(spec,
				`{"availableReplicas": 4}`),
			wantReady:     fnv1.Ready_READY_TRUE,
			wantRollout:   "Available",
			wantAvailable: 4,
		},
		"MissingImageIsFatal": {
			reason: "A request without spec.image cannot " +
				"be composed.",
			req: rolloutRequest(`{"replicas": 3}`, ""),
			wantFatal: "spec.image",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := &Function{log: logging.NewNopLogger()}
			rsp, err := f.RunFunction(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("%s\nRunFunction returned an error: %v",
					tc.reason, err)
			}

			fatal := fnv1.Severity_SEVERITY_FATAL
			if tc.wantFatal != "" {
				if !hasResult(rsp, fatal, tc.wantFatal) {
					t.Errorf("%s\nwant a fatal result containing "+
						"%q, got: %v", tc.reason, tc.wantFatal,
						rsp.GetResults())
				}
				return
			}
			if hasResult(rsp, fatal, "") {
				t.Fatalf("%s\nunexpected fatal result: %v",
					tc.reason, rsp.GetResults())
			}

			// The Deployment must be in the desired state on EVERY
			// call, or Crossplane would delete it.
			d, ok := rsp.GetDesired().GetResources()["deployment"]
			if !ok {
				t.Fatalf("%s\ndesired Deployment is missing",
					tc.reason)
			}
			if d.GetReady() != tc.wantReady {
				t.Errorf("%s\nreadiness: want %v, got %v",
					tc.reason, tc.wantReady, d.GetReady())
			}
			dSpec := d.GetResource().GetFields()["spec"]
			replicas := dSpec.GetStructValue().GetFields()["replicas"]
			if replicas.GetNumberValue() != 3 {
				t.Errorf("%s\nDeployment replicas: want 3, got %v",
					tc.reason, replicas.GetNumberValue())
			}

			rollout := xrStatus(rsp, "rolloutStatus").GetStringValue()
			if rollout != tc.wantRollout {
				t.Errorf("%s\nstatus.rolloutStatus: want %q, got %q",
					tc.reason, tc.wantRollout, rollout)
			}
			avail := xrStatus(rsp, "availableReplicas").GetNumberValue()
			if avail != tc.wantAvailable {
				t.Errorf("%s\nstatus.availableReplicas: want %v, "+
					"got %v", tc.reason, tc.wantAvailable, avail)
			}

			normal := fnv1.Severity_SEVERITY_NORMAL
			if tc.wantNormal != "" &&
				!hasResult(rsp, normal, tc.wantNormal) {
				t.Errorf("%s\nwant a normal result containing %q",
					tc.reason, tc.wantNormal)
			}
		})
	}
}
