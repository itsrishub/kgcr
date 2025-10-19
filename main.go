package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"sort"
	"sync"
	"text/tabwriter"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
)

type foundResource struct {
	crdName      string
	resourceName string
	instanceName string
	namespace    string // Add namespace field
}

type crdJob struct {
	crd           apiextensionsv1.CustomResourceDefinition
	storedVersion string                      // Pre-compute stored version
	gvr           schema.GroupVersionResource // Pre-compute GVR
}

func main() {
	namespace := flag.String("n", "", "the namespace to scan for custom resources. If not specified, the current context's namespace is used.")
	flag.StringVar(namespace, "namespace", "", "the namespace to scan for custom resources. If not specified, the current context's namespace is used.")
	allNamespaces := flag.Bool("A", false, "scan all namespaces")
	flag.BoolVar(allNamespaces, "all-namespaces", false, "scan all namespaces")
	timeout := flag.Duration("timeout", 30*time.Second, "timeout for the operation")
	flag.Parse()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()

	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	// Get the current context name
	rawConfig, err := kubeConfig.RawConfig()
	if err != nil {
		log.Fatalf("Error loading kubeconfig: %s", err.Error())
	}

	currentContextName := rawConfig.CurrentContext
	// log.Printf("Using context: %s", currentContextName)

	// Build the rest config using the current context
	config, err := kubeConfig.ClientConfig()
	if err != nil {
		panic(err.Error())
	}

	// Increase QPS and Burst to avoid client-side throttling
	config.QPS = 100
	config.Burst = 200

	// If the namespace flag is not set, get it from the current context
	if *namespace == "" && !*allNamespaces {
		// Get namespace from the current context
		currentContext := rawConfig.Contexts[currentContextName]
		if currentContext != nil && currentContext.Namespace != "" {
			*namespace = currentContext.Namespace
		} else {
			// If no namespace in context, default to "default"
			*namespace = "default"
		}
	}

	// If allNamespaces is set, clear the namespace to scan all
	if *allNamespaces {
		*namespace = ""
	}

	// log.Printf("Scanning namespace: %s", *namespace)

	// Suppress deprecation warnings
	config.WarningHandler = rest.NewWarningWriter(io.Discard, rest.WarningWriterOptions{})

	// Apiextensions client to list all the CRDs
	apiextensionsClient, err := apiextensionsclientset.NewForConfig(config)
	if err != nil {
		log.Fatalf("Error creating apiextensions client: %s", err.Error())
	}

	// Dynamic client to fetch instances of the CRDs
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		log.Fatalf("Error creating dynamic client: %s", err.Error())
	}

	// List all CRDs in the cluster ---
	crdList, err := apiextensionsClient.ApiextensionsV1().CustomResourceDefinitions().List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Fatalf("Error listing CRDs: %s", err.Error())
	}

	// Pre-process CRDs and filter out cluster-scoped resources
	var namespacedCRDs []crdJob
	for _, crd := range crdList.Items {
		// Skip cluster-scoped resources
		if crd.Spec.Scope != "Namespaced" {
			continue
		}

		storedVersion := getStoredVersion(&crd)
		if storedVersion == "" {
			continue
		}

		gvr := schema.GroupVersionResource{
			Group:    crd.Spec.Group,
			Version:  storedVersion,
			Resource: crd.Spec.Names.Plural,
		}

		namespacedCRDs = append(namespacedCRDs, crdJob{
			crd:           crd,
			storedVersion: storedVersion,
			gvr:           gvr,
		})
	}

	if len(namespacedCRDs) == 0 {
		fmt.Printf("No namespaced custom resources found in cluster\n")
		return
	}

	// Create buffered channels for better throughput
	jobs := make(chan crdJob, len(namespacedCRDs))
	results := make(chan []foundResource, len(namespacedCRDs))

	// Determine optimal number of workers
	numWorkers := runtime.NumCPU() * 3
	if numWorkers > len(namespacedCRDs) {
		numWorkers = len(namespacedCRDs)
	}
	if numWorkers > 20 {
		numWorkers = 20 // Cap at 20 to avoid overwhelming the API server
	}

	// Start worker goroutines
	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go crdWorker(ctx, w, jobs, results, dynamicClient, *namespace, *allNamespaces, &wg)
	}

	// Send jobs to workers
	for _, job := range namespacedCRDs {
		select {
		case jobs <- job:
		case <-ctx.Done():
			close(jobs)
			log.Fatalf("Timeout while sending jobs: %v", ctx.Err())
		}
	}
	close(jobs)

	// Wait for all workers to finish
	go func() {
		wg.Wait()
		close(results)
	}()

	// Pre-allocate result slice with estimated capacity
	allResults := make([]foundResource, 0, len(namespacedCRDs)*10)

	// Collect all results
	for workerResults := range results {
		allResults = append(allResults, workerResults...)
	}

	// Sort the results alphabetically by CRD name, then by resource name, then by instance name
	sort.Slice(allResults, func(i, j int) bool {
		if allResults[i].crdName != allResults[j].crdName {
			return allResults[i].crdName < allResults[j].crdName
		}
		if allResults[i].resourceName != allResults[j].resourceName {
			return allResults[i].resourceName < allResults[j].resourceName
		}
		if allResults[i].namespace != allResults[j].namespace {
			return allResults[i].namespace < allResults[j].namespace
		}
		return allResults[i].instanceName < allResults[j].instanceName
	})

	if len(allResults) > 0 {
		w := new(tabwriter.Writer)
		w.Init(os.Stdout, 0, 8, 1, '\t', 0)
		if *allNamespaces {
			fmt.Fprintln(w, "NAMESPACE\tCRD\tRESOURCE\tNAME")
			for _, res := range allResults {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", res.namespace, res.crdName, res.resourceName, res.instanceName)
			}
		} else {
			fmt.Fprintln(w, "CRD\tRESOURCE\tNAME")
			for _, res := range allResults {
				fmt.Fprintf(w, "%s\t%s\t%s\n", res.crdName, res.resourceName, res.instanceName)
			}
		}
		w.Flush()
	} else {
		if *allNamespaces {
			fmt.Printf("No custom resources found in any namespace\n")
		} else {
			fmt.Printf("No custom resources found in namespace: %s\n", *namespace)
		}
	}
}

// crdWorker processes CRD jobs concurrently
func crdWorker(ctx context.Context, id int, jobs <-chan crdJob, results chan<- []foundResource, dynamicClient dynamic.Interface, namespace string, allNamespaces bool, wg *sync.WaitGroup) {
	defer wg.Done()

	// Pre-allocate a reusable slice for results
	workerResults := make([]foundResource, 0, 50)

	for job := range jobs {
		// Check context cancellation
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Clear the slice but keep the underlying array
		workerResults = workerResults[:0]

		// Use pre-computed GVR
		gvr := job.gvr

		// Create a sub-context with a shorter timeout for individual requests
		reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)

		// Use the dynamic client to list all instances of the CRD in the specified namespace
		var resourceList *unstructured.UnstructuredList
		var err error
		if allNamespaces || namespace == "" {
			// List across all namespaces
			resourceList, err = dynamicClient.Resource(gvr).List(reqCtx, metav1.ListOptions{})
		} else {
			// List in specific namespace
			resourceList, err = dynamicClient.Resource(gvr).Namespace(namespace).List(reqCtx, metav1.ListOptions{})
		}
		cancel()

		if err != nil {
			// Skip CRDs that error out
			continue
		}

		if len(resourceList.Items) > 0 {
			// Pre-allocate with exact size
			if cap(workerResults) < len(resourceList.Items) {
				workerResults = make([]foundResource, 0, len(resourceList.Items))
			}

			for _, item := range resourceList.Items {
				workerResults = append(workerResults, foundResource{
					crdName:      job.crd.Name,
					resourceName: gvr.Resource,
					instanceName: item.GetName(),
					namespace:    item.GetNamespace(),
				})
			}
		}

		if len(workerResults) > 0 {
			// Create a copy to send through the channel
			resultsCopy := make([]foundResource, len(workerResults))
			copy(resultsCopy, workerResults)

			select {
			case results <- resultsCopy:
			case <-ctx.Done():
				return
			}
		}
	}
}

// getStoredVersion finds the version that is marked for storage.
// This is typically the most stable or preferred version of the CRD.
func getStoredVersion(crd *apiextensionsv1.CustomResourceDefinition) string {
	for _, version := range crd.Spec.Versions {
		if version.Storage {
			return version.Name
		}
	}
	// Fallback to the first version if no storage version is explicitly set
	if len(crd.Spec.Versions) > 0 {
		return crd.Spec.Versions[0].Name
	}
	return ""
}
