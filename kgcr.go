package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"path/filepath"

	// Kubernetes API Machinery
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	// Kubernetes Client-Go libraries
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	// Apiextensions client for listing CRDs
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
)

func main() {
	// Setup Kubeconfig and command-line flags ---
	var kubeconfig *string
	// Default to home directory's kubeconfig
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}

	// Define a namespace flag with "default" as the default value
	namespace := flag.String("namespace", "", "the namespace to scan for custom resources. If not specified, the current context's namespace is used.")
	flag.Parse()

	// log.Printf("Scanning for all custom resources in namespace: %s\n", *namespace)

	// use the current context in kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	// If the namespace flag is not set, get it from the context
	if *namespace == "" {
		clientCfg, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
		if err != nil {
			log.Fatalf("Error loading kubeconfig: %s", err.Error())
		}
		currentContext := clientCfg.Contexts[clientCfg.CurrentContext]
		*namespace = currentContext.Namespace
		// If the namespace is still empty, default to "default"
		if *namespace == "" {
			*namespace = "default"
		}
	}

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

	// log.Printf("Found %d CRDs in the cluster.\n\n", len(crdList.Items))

	var flag int = 0

	// Iterate through each CRD and list its instances ---
	for _, crd := range crdList.Items {
		// For each CRD, we need to construct its GroupVersionResource (GVR)
		// This GVR is the "address" the dynamic client will use to find the resource instances.
		gvr := schema.GroupVersionResource{
			Group:    crd.Spec.Group,
			Version:  getStoredVersion(&crd), // Use the primary stored version
			Resource: crd.Spec.Names.Plural,
		}

		// Use the dynamic client to list all instances of the CRD in the specified namespace
		resourceList, err := dynamicClient.Resource(gvr).Namespace(*namespace).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			log.Printf("  -> Error listing resources for GVR %s: %s\n", gvr, err.Error())
			continue // Move to the next CRD
		}

		if len(resourceList.Items) != 0 {
			fmt.Printf("CRD\tCR\tResource\n")
			for _, item := range resourceList.Items {
				fmt.Printf("%s\t%s\t%s\n", crd.Name, gvr, item.GetName())
			}
		} else {
			flag = 1
		}
	}
	if flag == 1 {
		fmt.Printf("No custom resources found in namespace: %s\n", *namespace)
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
