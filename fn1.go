package main

import (
        "context"
        "fmt"

        "github.com/crossplane/function-sdk-go/errors"
        "github.com/crossplane/function-sdk-go/logging"
        fnv1 "github.com/crossplane/function-sdk-go/proto/v1"
        "github.com/crossplane/function-sdk-go/request"
        "github.com/crossplane/function-sdk-go/resource"
        "github.com/crossplane/function-sdk-go/resource/composed"
        "github.com/crossplane/function-sdk-go/response"
)

// deploymentKey identifies the Deployment in both the desired
// and the observed composed resources.
const deploymentKey = resource.Name("deployment")

// Function composes a Deployment and tracks its rollout.
type Function struct {
        fnv1.UnimplementedFunctionRunnerServiceServer

        log logging.Logger
}

// RunFunction is called by Crossplane on every reconcile
// of a WebApp composite resource.
func (f *Function) RunFunction(_ context.Context,
        req *fnv1.RunFunctionRequest) (*fnv1.RunFunctionResponse, error) {

        rsp := response.To(req, response.DefaultTTL)

        // STEP 1: Read the observed XR (what the developer asked for).
        oxr, err := request.GetObservedCompositeResource(req)
        if err != nil {
                response.Fatal(rsp, errors.Wrap(err,
                        "cannot read the observed composite resource"))
                return rsp, nil
        }

        image, err := oxr.Resource.GetString("spec.image")
        if err != nil {
                response.Fatal(rsp, errors.Wrap(err,
                        "cannot read spec.image"))
                return rsp, nil
        }

        target, err := oxr.Resource.GetInteger("spec.replicas")
        if err != nil {
                response.Fatal(rsp, errors.Wrap(err,
                        "cannot read spec.replicas"))
                return rsp, nil
        }

        // STEP 2: Build the desired Deployment. It is added on EVERY
        // call - a resource missing from desired state is deleted.
        appLabel := oxr.Resource.GetName()
        deploy := composed.New()
        deploy.Object = map[string]any{
                "apiVersion": "apps/v1",
                "kind":       "Deployment",
                "spec": map[string]any{
                        "replicas": target,
                        "selector": map[string]any{
                                "matchLabels": map[string]any{"app": appLabel},
                        },
                        "template": map[string]any{
                                "metadata": map[string]any{
                                        "labels": map[string]any{"app": appLabel},
                                },
                                "spec": map[string]any{
                                        "containers": []any{
                                                map[string]any{
                                                        "name":  "web",
                                                        "image": image,
                                                        "ports": []any{
                                                                map[string]any{"containerPort": int64(80)},
                                                        },
                                                        "readinessProbe": map[string]any{
                                                                "httpGet": map[string]any{
                                                                        "path": "/",
                                                                        "port": int64(80),
                                                                },
                                                                "initialDelaySeconds": int64(20),
                                                                "periodSeconds":       int64(5),
                                                        },
                                                },
                                        },
                                },
                        },
                },
        }

        // STEP 3: Read the observed composed resources.
        observed, err := request.GetObservedComposedResources(req)
        if err != nil {
                response.Fatal(rsp, errors.Wrap(err,
                        "cannot read the observed composed resources"))
                return rsp, nil
        }

        ready := resource.ReadyFalse
        var available int64
        var rollout string

        obsDeploy, exists := observed[deploymentKey]
        if !exists {
                // First reconcile: the Deployment is not created yet.
                rollout = "Deployment is being created"
                response.Normal(rsp,
                        "first reconcile: no observed Deployment yet")
        } else {
                // Kubernetes omits status.availableReplicas while it
                // is 0, so a missing field means 0 available.
                available, err = obsDeploy.Resource.GetInteger(
                        "status.availableReplicas")
                if err != nil {
                        available = 0
                }

                // STEP 4: The rule - compare observed with target.
                if available >= target {
                        ready = resource.ReadyTrue
                        rollout = "Available"
                } else {
                        rollout = fmt.Sprintf(
                                "Waiting: %d of %d replicas available",
                                available, target)
                }
        }

        // STEP 5: Report progress on the XR status.
        dxr, err := request.GetDesiredCompositeResource(req)
        if err != nil {
                response.Fatal(rsp, errors.Wrap(err,
                        "cannot read the desired composite resource"))
                return rsp, nil
        }
        dxr.Resource.SetAPIVersion(oxr.Resource.GetAPIVersion())
        dxr.Resource.SetKind(oxr.Resource.GetKind())
        _ = dxr.Resource.SetValue("status.availableReplicas",
                available)
        _ = dxr.Resource.SetValue("status.rolloutStatus", rollout)
        if err := response.SetDesiredCompositeResource(rsp,
                dxr); err != nil {
                response.Fatal(rsp, errors.Wrap(err,
                        "cannot set the desired composite resource"))
                return rsp, nil
        }

        // STEP 6: Return the Deployment with the readiness decision.
        desired, err := request.GetDesiredComposedResources(req)
        if err != nil {
                response.Fatal(rsp, errors.Wrap(err,
                        "cannot read the desired composed resources"))
                return rsp, nil
        }
        desired[deploymentKey] = &resource.DesiredComposed{
                Resource: deploy,
                Ready:    ready,
        }
        if err := response.SetDesiredComposedResources(rsp,
                desired); err != nil {
                response.Fatal(rsp, errors.Wrap(err,
                        "cannot set the desired composed resources"))
                return rsp, nil
        }

        f.log.Info("Rollout checked", "xr", oxr.Resource.GetName(),
                "available", available, "target", target)

        return rsp, nil
}
