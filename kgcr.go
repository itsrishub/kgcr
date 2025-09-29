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

	// Kubernetes API Machinery
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	// Kubernetes Client-Go libraries
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	// Apiextensions client for listing CRDs
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
)

type foundResource struct {
	crdName      string
	resourceName string
	instanceName string
}

type crdJob struct {
	crd apiextensionsv1.CustomResourceDefinition
}

func main() {
	// Setup Kubeconfig and command-line flags ---
	kubeconfig := flag.String("kubeconfig", "", "(optional) absolute path to the kubeconfig file. If not set, uses KUBECONFIG env var or default locations")

	// Define a namespace flag with "default" as the default value
	namespace := flag.String("namespace", "", "the namespace to scan for custom resources. If not specified, the current context's namespace is used.")
	flag.Parse()

	// Load the kubeconfig to get current context information
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	// Only set explicit path if kubeconfig flag is provided
	if *kubeconfig != "" {
		loadingRules.ExplicitPath = *kubeconfig
	}

	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	// Get the current context name
	rawConfig, err := kubeConfig.RawConfig()
	if err != nil {
		log.Fatalf("Error loading kubeconfig: %s", err.Error())
	}

	currentContextName := rawConfig.CurrentContext
	log.Printf("Using context: %s", currentContextName)

	// Build the rest config using the current context
	config, err := kubeConfig.ClientConfig()
	if err != nil {
		panic(err.Error())
	}

	// Increase QPS and Burst to avoid client-side throttling
	config.QPS = 50
	config.Burst = 100

	// If the namespace flag is not set, get it from the current context
	if *namespace == "" {
		// Get namespace from the current context
		currentContext := rawConfig.Contexts[currentContextName]
		if currentContext != nil && currentContext.Namespace != "" {
			*namespace = currentContext.Namespace
		} else {
			// If no namespace in context, default to "default"
			*namespace = "default"
		}
	}

	log.Printf("Scanning namespace: %s", *namespace)

	// Suppress deprecation warnings
	config.WarningHandler = rest.NewWarningWriter(io.Discard, rest.WarningWriterOptions{})

	// Create the necessary clients ---

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
	crdList, err := apiextensionsClient.ApiextensionsV1().CustomResourceDefinitions().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		log.Fatalf("Error listing CRDs: %s", err.Error())
	}

	log.Printf("Found %d CRDs in the cluster.", len(crdList.Items))

	// Create channels for job distribution and results
	jobs := make(chan crdJob, len(crdList.Items))
	results := make(chan []foundResource, len(crdList.Items))

	// Determine number of workers based on CPU cores
	numWorkers := runtime.NumCPU() * 2
	if numWorkers > len(crdList.Items) {
		numWorkers = len(crdList.Items)
	}

	// Start worker goroutines
	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go crdWorker(w, jobs, results, dynamicClient, *namespace, &wg)
	}

	// Send jobs to workers
	for _, crd := range crdList.Items {
		jobs <- crdJob{crd: crd}
	}
	close(jobs)

	// Wait for all workers to finish
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect all results
	var allResults []foundResource
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
		return allResults[i].instanceName < allResults[j].instanceName
	})

	if len(allResults) > 0 {
		w := new(tabwriter.Writer)
		w.Init(os.Stdout, 0, 8, 1, '\t', 0)
		fmt.Fprintln(w, "CRD\tRESOURCE\tNAME")
		for _, res := range allResults {
			fmt.Fprintf(w, "%s\t%s\t%s\n", res.crdName, res.resourceName, res.instanceName)
		}
		w.Flush()
	} else {
		fmt.Printf("No custom resources found in namespace: %s\n", *namespace)
	}
}

// crdWorker processes CRD jobs concurrently
func crdWorker(id int, jobs <-chan crdJob, results chan<- []foundResource, dynamicClient dynamic.Interface, namespace string, wg *sync.WaitGroup) {
	defer wg.Done()

	for job := range jobs {
		crd := job.crd
		var workerResults []foundResource

		// For each CRD, we need to construct its GroupVersionResource (GVR)
		gvr := schema.GroupVersionResource{
			Group:    crd.Spec.Group,
			Version:  getStoredVersion(&crd),
			Resource: crd.Spec.Names.Plural,
		}

		// Use the dynamic client to list all instances of the CRD in the specified namespace
		resourceList, err := dynamicClient.Resource(gvr).Namespace(namespace).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			// Skip CRDs that error out (e.g., cluster-scoped resources when querying namespace)
			continue
		}

		if len(resourceList.Items) > 0 {
			for _, item := range resourceList.Items {
				workerResults = append(workerResults, foundResource{
					crdName:      crd.Name,
					resourceName: gvr.Resource,
					instanceName: item.GetName(),
				})
			}
		}

		if len(workerResults) > 0 {
			results <- workerResults
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
