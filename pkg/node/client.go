package node

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"sigs.k8s.io/container-object-storage-interface-api/apis/objectstorage.k8s.io/v1alpha1"
	cs "sigs.k8s.io/container-object-storage-interface-api/clientset/typed/objectstorage.k8s.io/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	podNameKey      = "csi.storage.k8s.io/pod.name"
	podNamespaceKey = "csi.storage.k8s.io/pod.namespace"

	barNameKey      = "bar-name"
	barNamespaceKey = "bar-namespace"
)

type NodeClient struct {
	cosiClient *cs.ObjectstorageV1alpha1Client
	kubeClient kubernetes.Interface
}

func NewClientOrDie() *NodeClient {
	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}
	// The following function calls may panic based on the config
	client := cs.NewForConfigOrDie(config)
	kube := kubernetes.NewForConfigOrDie(config)
	return &NodeClient{
		cosiClient: client,
		kubeClient: kube,
	}
}

func parseValue(key string, volCtx map[string]string) (string, error) {
	value, ok := volCtx[key]
	if !ok {
		return "", fmt.Errorf("required volume context key unset: %v", key)
	}
	return value, nil
}

func parseVolumeContext(volCtx map[string]string) (barname, barns, podname, podns string, err error) {
	klog.Info("parsing bucketAccessRequest namespace/name from volume context")
	if barname, err = parseValue(barNameKey, volCtx); err != nil {
		return "", "", "", "", err
	}
	if barns, err = parseValue(barNamespaceKey, volCtx); err != nil {
		return "", "", "", "", err
	}
	if podname, err = parseValue(podNameKey, volCtx); err != nil {
		return "", "", "", "", err
	}
	if podns, err = parseValue(podNamespaceKey, volCtx); err != nil {
		return "", "", "", "", err
	}
	return barname, barns, podname, podns, nil
}

func (n *NodeClient) getBAR(ctx context.Context, barName, barNs string) (*v1alpha1.BucketAccessRequest, error) {
	klog.Infof("getting bucketAccessRequest %q", fmt.Sprintf("%s/%s", barNs, barName))
	bar, err := n.cosiClient.BucketAccessRequests(barNs).Get(ctx, barName, metav1.GetOptions{})
	if err != nil {
		return nil, errors.Wrap(err, "get bucketAccessRequest failed")
	}
	if bar == nil {
		return nil, fmt.Errorf("bucketAccessRequest is nil %q", fmt.Sprintf("%s/%s", barNs, barName))
	}
	if !bar.Status.AccessGranted {
		return nil, fmt.Errorf("bucketAccessRequest does not grant access %q", fmt.Sprintf("%s/%s", barNs, barName))
	}
	if len(bar.Spec.BucketRequestName) == 0 {
		return nil, fmt.Errorf("bucketAccessRequest.Spec.BucketRequestName unset")
	}
	if len(bar.Status.BucketAccessName) == 0 {
		return nil, fmt.Errorf("bucketAccessRequest.Spec.BucketAccessName unset")
	}
	return bar, nil
}

func (n *NodeClient) getBA(ctx context.Context, baName string) (*v1alpha1.BucketAccess, error) {
	klog.Infof("getting bucketAccess %q", fmt.Sprintf("%s", baName))
	ba, err := n.cosiClient.BucketAccesses().Get(ctx, baName, metav1.GetOptions{})
	if err != nil {
		return nil, logErr(getError("bucketAccess", baName, err))
	}
	if ba == nil {
		return nil, logErr(fmt.Errorf("bucketAccess is nil %q", fmt.Sprintf("%s", baName)))
	}
	if !ba.Status.AccessGranted {
		return nil, logErr(fmt.Errorf("bucketAccess does not grant access %q", fmt.Sprintf("%s", baName)))
	}
	if len(ba.Spec.MintedSecretName) == 0 {
		return nil, logErr(fmt.Errorf("bucketAccess.Spec.MintedSecretName unset"))
	}
	return ba, nil
}

func (n *NodeClient) getBR(ctx context.Context, brName, brNs string) (*v1alpha1.BucketRequest, error) {
	klog.Infof("getting bucketRequest %q", brName)
	br, err := n.cosiClient.BucketRequests(brNs).Get(ctx, brName, metav1.GetOptions{})
	if err != nil {
		return nil, logErr(getError("bucketRequest", fmt.Sprintf("%s/%s", brNs, brName), err))
	}
	if br == nil {
		return nil, logErr(fmt.Errorf("bucketRequest is nil %q", fmt.Sprintf("%s/%s", brNs, brName)))
	}
	if !br.Status.BucketAvailable {
		return nil, logErr(fmt.Errorf("bucketRequest is not available yet %q", fmt.Sprintf("%s/%s", brNs, brName)))
	}
	if len(br.Status.BucketName) == 0 {
		return nil, logErr(fmt.Errorf("bucketRequest.Spec.BucketInstanceName unset"))
	}
	return br, nil
}

func (n *NodeClient) getB(ctx context.Context, bName string) (*v1alpha1.Bucket, error) {
	klog.Infof("getting bucket %q", bName)
	// is BucketInstanceName the correct field, or should it be BucketClass
	bkt, err := n.cosiClient.Buckets().Get(ctx, bName, metav1.GetOptions{})
	if err != nil {
		return nil, logErr(getError("bucket", bName, err))
	}
	if bkt == nil {
		return nil, logErr(fmt.Errorf("bucket is nil %q", fmt.Sprintf("%s", bName)))
	}
	if !bkt.Status.BucketAvailable {
		return nil, logErr(fmt.Errorf("bucket is not available yet %q", fmt.Sprintf("%s", bName)))
	}
	return bkt, nil
}

func (n *NodeClient) GetResources(ctx context.Context, barName, barNs string) (bkt *v1alpha1.Bucket, ba *v1alpha1.BucketAccess, secret *v1.Secret, err error) {
	var bar *v1alpha1.BucketAccessRequest

	if bar, err = n.getBAR(ctx, barName, barNs); err != nil {
		return
	}

	if ba, err = n.getBA(ctx, bar.Status.BucketAccessName); err != nil {
		return
	}

	if bkt, err = n.getB(ctx, ba.Spec.BucketName); err != nil {
		return
	}

	if secret, err = n.kubeClient.CoreV1().Secrets(barNs).Get(ctx, ba.Spec.MintedSecretName, metav1.GetOptions{}); err != nil {
		_ = logErr(getError("secret", fmt.Sprintf("%s/%s", barNs, ba.Spec.MintedSecretName), err))
		return
	}
	return
}

func (n *NodeClient) getProtocol(bkt *v1alpha1.Bucket) (data []byte, err error) {
	klog.Infof("bucket protocol %+v", bkt.Spec.Protocol)
	var protocolConnection interface{}
	switch {
	case bkt.Spec.Protocol.S3 != nil:
		protocolConnection = bkt.Spec.Protocol.S3
	case bkt.Spec.Protocol.AzureBlob != nil:
		protocolConnection = bkt.Spec.Protocol.AzureBlob
	case bkt.Spec.Protocol.GCS != nil:
		protocolConnection = bkt.Spec.Protocol.GCS
	default:
		err = fmt.Errorf("unrecognized protocol %+v, unable to extract connection data", bkt.Spec.Protocol)
	}

	if err != nil {
		return nil, logErr(err)
	}

	if data, err = json.Marshal(protocolConnection); err != nil {
		return nil, logErr(err)
	}
	return data, nil
}

func (n *NodeClient) addBAFinalizer(ctx context.Context, ba *v1alpha1.BucketAccess, BAFinalizer string) error {
	controllerutil.AddFinalizer(ba, BAFinalizer)
	if _, err := n.cosiClient.BucketAccesses().Update(ctx, ba, metav1.UpdateOptions{}); err != nil {
		return err
	}
	return nil
}

func (n *NodeClient) removeBAFinalizer(ctx context.Context, ba *v1alpha1.BucketAccess, BAFinalizer string) error {
	controllerutil.RemoveFinalizer(ba, BAFinalizer)
	if _, err := n.cosiClient.BucketAccesses().Update(ctx, ba, metav1.UpdateOptions{}); err != nil {
		return err
	}
	return nil
}
