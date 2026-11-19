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

// webAppRequest is a request fixture. It wraps the supplied
// spec (as JSON) in an observed WebApp XR.
func webAppRequest(spec string) *fnv1.RunFunctionRequest {
        return &fnv1.RunFunctionRequest{
                Observed: &fnv1.State{
                        Composite: &fnv1.Resource{
                                Resource: resource.MustStructJSON(`{
                                        "apiVersion": "platform.example.com/v1alpha1",
                                        "kind": "WebApp",
                                        "metadata": {"name": "test", "namespace": "team-test"},
                                        "spec": ` + spec + `
                                }`),
                        },
                },
        }
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
        cases := map[string]struct {
                reason      string
                req         *fnv1.RunFunctionRequest
                wantFatal   string
                wantWarning string
                wantName    string
        }{
                "ValidProductionRequest": {
                        reason: "A clean prod request with enough replicas " +
                                "is accepted unchanged.",
                        req: webAppRequest(`{"appName": "payments",
                                "environment": "prod", "replicas": 3}`),
                        wantName: "payments",
                },
                "NormalizesSpacesAndCase": {
                        reason: "Outer spaces are trimmed and capitals " +
                                "are lowercased.",
                        req: webAppRequest(`{"appName": "  Inventory-API  ",
                                "environment": "dev", "replicas": 2}`),
                        wantName: "inventory-api",
                },
                "ProductionAtMinimumIsAllowed": {
                        reason: "Exactly 2 replicas in prod meets the rule " +
                                "(boundary value).",
                        req: webAppRequest(`{"appName": "orders",
                                "environment": "prod", "replicas": 2}`),
                        wantName: "orders",
                },
                "ProductionBelowMinimumIsFatal": {
                        reason: "1 replica in prod breaks the hard rule.",
                        req: webAppRequest(`{"appName": "orders",
                                "environment": "prod", "replicas": 1}`),
                        wantFatal: "at least 2 replicas",
                },
                "DevelopmentAboveThreeWarns": {
                        reason: "5 replicas in dev triggers the soft rule " +
                                "but is still composed.",
                        req: webAppRequest(`{"appName": "search",
                                "environment": "dev", "replicas": 5}`),
                        wantWarning: "at most 3 replicas",
                        wantName:    "search",
                },
                "MissingReplicasIsFatal": {
                        reason: "A request without spec.replicas cannot " +
                                "be evaluated.",
                        req: webAppRequest(`{"appName": "billing",
                                "environment": "dev"}`),
                        wantFatal: "spec.replicas",
                },
                "BlankNameIsFatal": {
                        reason: "A name made only of spaces is empty " +
                                "after normalization.",
                        req: webAppRequest(`{"appName": "   ",
                                "environment": "dev", "replicas": 1}`),
                        wantFatal: "must not be empty",
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

                        // Fatal result: expected exactly when wantFatal is set.
                        fatal := fnv1.Severity_SEVERITY_FATAL
                        if tc.wantFatal == "" && hasResult(rsp, fatal, "") {
                                t.Errorf("%s\nunexpected fatal result: %v",
                                        tc.reason, rsp.GetResults())
                        }
                        if tc.wantFatal != "" &&
                                !hasResult(rsp, fatal, tc.wantFatal) {
                                t.Errorf("%s\nwant a fatal result containing %q, "+
                                        "got: %v", tc.reason, tc.wantFatal,
                                        rsp.GetResults())
                        }

                        // Warning: expected exactly when wantWarning is set.
                        warn := fnv1.Severity_SEVERITY_WARNING
                        if tc.wantWarning == "" && hasResult(rsp, warn, "") {
                                t.Errorf("%s\nunexpected warning: %v",
                                        tc.reason, rsp.GetResults())
                        }
                        if tc.wantWarning != "" &&
                                !hasResult(rsp, warn, tc.wantWarning) {
                                t.Errorf("%s\nwant a warning containing %q, "+
                                        "got: %v", tc.reason, tc.wantWarning,
                                        rsp.GetResults())
                        }

                        cm, composed := rsp.GetDesired().GetResources()["app-settings"]

                        // A rejected request must not compose anything.
                        if tc.wantFatal != "" {
                                if composed {
                                        t.Errorf("%s\nConfigMap composed for a "+
                                                "rejected request", tc.reason)
                                }
                                return
                        }

                        // An accepted request: check normalization results.
                        got := xrStatus(rsp, "appName").GetStringValue()
                        if got != tc.wantName {
                                t.Errorf("%s\nstatus.appName: want %q, got %q",
                                        tc.reason, tc.wantName, got)
                        }
                        if !composed {
                                t.Fatalf("%s\nConfigMap app-settings is missing",
                                        tc.reason)
                        }
                        meta := cm.GetResource().GetFields()["metadata"]
                        cmName := meta.GetStructValue().GetFields()["name"]
                        if cmName.GetStringValue() != tc.wantName {
                                t.Errorf("%s\nConfigMap name: want %q, got %q",
                                        tc.reason, tc.wantName,
                                        cmName.GetStringValue())
                        }
                        if cm.GetReady() != fnv1.Ready_READY_TRUE {
                                t.Errorf("%s\nConfigMap should be marked ready",
                                        tc.reason)
                        }
                })
        }
}
