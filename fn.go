package main

import (
	"context"
	"strconv"
	"strings"

	"github.com/crossplane/function-sdk-go/errors"
	"github.com/crossplane/function-sdk-go/logging"
	fnv1 "github.com/crossplane/function-sdk-go/proto/v1"
	"github.com/crossplane/function-sdk-go/request"
	"github.com/crossplane/function-sdk-go/resource"
	"github.com/crossplane/function-sdk-go/resource/composed"
	"github.com/crossplane/function-sdk-go/response"
)

// Function validates and normalizes WebApp requests.
type Function struct {
	fnv1.UnimplementedFunctionRunnerServiceServer

	log logging.Logger
}

// RunFunction is called by Crossplane every time a WebApp
// composite resource is reconciled.
func (f *Function) RunFunction(_ context.Context,
	req *fnv1.RunFunctionRequest) (*fnv1.RunFunctionResponse, error) {

	f.log.Info("Running function", "tag", req.GetMeta().GetTag())

	// Start the response from the request. This keeps any
	// desired state added by earlier pipeline steps.
	rsp := response.To(req, response.DefaultTTL)

	// STEP 1: Read the XR exactly as the developer submitted it.
	oxr, err := request.GetObservedCompositeResource(req)
	if err != nil {
		response.Fatal(rsp, errors.Wrap(err,
			"cannot read the observed composite resource"))
		return rsp, nil
	}

	rawName, err := oxr.Resource.GetString("spec.appName")
	if err != nil {
		response.Fatal(rsp, errors.Wrap(err,
			"cannot read spec.appName"))
		return rsp, nil
	}

	env, err := oxr.Resource.GetString("spec.environment")
	if err != nil {
		response.Fatal(rsp, errors.Wrap(err,
			"cannot read spec.environment"))
		return rsp, nil
	}

	replicas, err := oxr.Resource.GetInteger("spec.replicas")
	if err != nil {
		response.Fatal(rsp, errors.Wrap(err,
			"cannot read spec.replicas"))
		return rsp, nil
	}

	// STEP 2: Normalize - remove outer spaces, use lowercase.
	appName := strings.ToLower(strings.TrimSpace(rawName))
	if appName == "" {
		response.Fatal(rsp, errors.New(
			"spec.appName must not be empty or only spaces"))
		return rsp, nil
	}

	// STEP 3: Business rule - replicas must suit the environment.
	// Hard rule: production needs at least 2 replicas.
	if env == "prod" && replicas < 2 {
		response.Fatal(rsp, errors.Errorf(
			"environment prod needs at least 2 replicas "+
				"for high availability, but spec.replicas is %d",
			replicas))
		return rsp, nil
	}

	// Soft rule: development rarely needs more than 3 replicas.
	if env == "dev" && replicas > 3 {
		response.Warning(rsp, errors.Errorf(
			"environment dev normally needs at most 3 replicas, "+
				"but spec.replicas is %d; review the cost",
			replicas)).TargetComposite()
	}

	// STEP 4: Publish the clean name in the XR status.
	dxr, err := request.GetDesiredCompositeResource(req)
	if err != nil {
		response.Fatal(rsp, errors.Wrap(err,
			"cannot read the desired composite resource"))
		return rsp, nil
	}
	dxr.Resource.SetAPIVersion(oxr.Resource.GetAPIVersion())
	dxr.Resource.SetKind(oxr.Resource.GetKind())
	if err := dxr.Resource.SetValue("status.appName",
		appName); err != nil {
		response.Fatal(rsp, errors.Wrap(err,
			"cannot set status.appName"))
		return rsp, nil
	}
	if err := response.SetDesiredCompositeResource(rsp,
		dxr); err != nil {
		response.Fatal(rsp, errors.Wrap(err,
			"cannot set the desired composite resource"))
		return rsp, nil
	}

	// STEP 5: Compose a ConfigMap that uses the clean name.
	desired, err := request.GetDesiredComposedResources(req)
	if err != nil {
		response.Fatal(rsp, errors.Wrap(err,
			"cannot read the desired composed resources"))
		return rsp, nil
	}

	cm := composed.New()
	cm.Object = map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name": appName,
		},
		"data": map[string]any{
			"appName":     appName,
			"environment": env,
			"replicas":    strconv.FormatInt(replicas, 10),
		},
	}

	desired[resource.Name("app-settings")] =
		&resource.DesiredComposed{
			Resource: cm,
			Ready:    resource.ReadyTrue,
		}

	if err := response.SetDesiredComposedResources(rsp,
		desired); err != nil {
		response.Fatal(rsp, errors.Wrap(err,
			"cannot set the desired composed resources"))
		return rsp, nil
	}

	response.Normalf(rsp,
		"application name normalized from %q to %q",
		rawName, appName)

	f.log.Info("WebApp input accepted", "appName", appName,
		"environment", env, "replicas", replicas)

	return rsp, nil
}
