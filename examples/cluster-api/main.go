package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	addonsv1 "sigs.k8s.io/cluster-api/api/addons/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/internal/controllers/clusterresourcesetbinding"
	"github.com/tgoodwin/kamera/pkg/coverage"
	"github.com/tgoodwin/kamera/pkg/explore"
	"github.com/tgoodwin/kamera/pkg/tracecheck"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var scheme = runtime.NewScheme()

func init() {
	_ = clusterv1.AddToScheme(scheme)
	_ = addonsv1.AddToScheme(scheme)
}

func newBuilder() *tracecheck.ExplorerBuilder {
	eb := tracecheck.NewExplorerBuilder(scheme)
	eb.WithReconciler("ClusterResourceSetBindingController", func(c ctrlclient.Client) tracecheck.Reconciler {
		return &clusterresourcesetbinding.Reconciler{Client: c}
	}).For("addons.cluster.x-k8s.io/ClusterResourceSetBinding")
	return eb
}

var bindingGK = schema.GroupKind{Group: "addons.cluster.x-k8s.io", Kind: "ClusterResourceSetBinding"}

func scenariosFromInputs(builder *tracecheck.ExplorerBuilder, inputs []coverage.Input) ([]explore.Scenario, error) {
	scenarios := make([]explore.Scenario, 0, len(inputs))
	for _, input := range inputs {
		objects := make([]ctrlclient.Object, 0, len(input.EnvironmentState.Objects))
		for _, obj := range input.EnvironmentState.Objects {
			if obj == nil {
				continue
			}
			objects = append(objects, obj)
		}

		var bindingName string
		for _, obj := range objects {
			if obj.GetObjectKind().GroupVersionKind().GroupKind() == bindingGK {
				bindingName = obj.GetName()
				break
			}
		}
		if bindingName == "" {
			return nil, fmt.Errorf("scenario %q: no ClusterResourceSetBinding found in objects", input.Name)
		}

		state, err := builder.BuildStartStateFromObjects(
			objects,
			[]tracecheck.PendingReconcile{{
				ReconcilerID: "ClusterResourceSetBindingController",
				Request: reconcile.Request{
					NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: bindingName},
				},
				Source: tracecheck.SourceStateChange,
			}},
		)
		if err != nil {
			return nil, fmt.Errorf("scenario %q: build state: %w", input.Name, err)
		}

		cfg, err := explore.ApplyInputTuning(builder.Config(), input.Tuning)
		if err != nil {
			return nil, fmt.Errorf("scenario %q: apply tuning: %w", input.Name, err)
		}

		scenarios = append(scenarios, explore.Scenario{
			Name:             input.Name,
			EnvironmentState: state,
			Config:           cfg,
		})
	}
	return scenarios, nil
}

func main() {
	flag.Parse()
	ctrl.SetLogger(ctrl.Log)

	inputsPath := explore.InputsPath()
	if inputsPath == "" {
		inputsPath = "inputs/inputs.json"
	}

	inputs, err := coverage.LoadInputs(inputsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load inputs: %v\n", err)
		os.Exit(1)
	}

	builder := newBuilder()

	scenarios, err := scenariosFromInputs(builder, inputs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build scenarios: %v\n", err)
		os.Exit(1)
	}

	if !explore.InteractiveEnabled() {
		runner, err := explore.NewParallelRunner(builder)
		if err != nil {
			fmt.Fprintf(os.Stderr, "runner setup: %v\n", err)
			os.Exit(1)
		}
		if _, err := runner.RunAll(context.Background(), scenarios, explore.ParallelOptions{DumpDir: explore.DumpPath()}); err != nil {
			fmt.Fprintf(os.Stderr, "run: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if len(scenarios) != 1 {
		fmt.Fprintf(os.Stderr, "interactive mode requires exactly one scenario; got %d — use --inputs with a single-scenario file\n", len(scenarios))
		os.Exit(1)
	}

	runner, err := explore.NewRunner(builder)
	if err != nil {
		fmt.Fprintf(os.Stderr, "runner setup: %v\n", err)
		os.Exit(1)
	}
	if err := runner.Run(context.Background(), explore.RunInput{EnvironmentState: scenarios[0].EnvironmentState}); err != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", err)
		os.Exit(1)
	}
}
