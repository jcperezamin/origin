package imagepolicy

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/golang/glog"
	lru "github.com/hashicorp/golang-lru"

	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
	kapi "k8s.io/kubernetes/pkg/api"

	"github.com/openshift/origin/pkg/api/latest"
	"github.com/openshift/origin/pkg/api/meta"
	"github.com/openshift/origin/pkg/client"
	oadmission "github.com/openshift/origin/pkg/cmd/server/admission"
	configlatest "github.com/openshift/origin/pkg/cmd/server/api/latest"
	"github.com/openshift/origin/pkg/image/admission/imagepolicy/api"
	"github.com/openshift/origin/pkg/image/admission/imagepolicy/api/validation"
	"github.com/openshift/origin/pkg/image/admission/imagepolicy/rules"
	imageapi "github.com/openshift/origin/pkg/image/api"
	"github.com/openshift/origin/pkg/project/cache"
)

func init() {
	admission.RegisterPlugin(api.PluginName, func(input io.Reader) (admission.Interface, error) {
		obj, err := configlatest.ReadYAML(input)
		if err != nil {
			return nil, err
		}
		if obj == nil {
			return nil, nil
		}
		config, ok := obj.(*api.ImagePolicyConfig)
		if !ok {
			return nil, fmt.Errorf("unexpected config object: %#v", obj)
		}
		if errs := validation.Validate(config); len(errs) > 0 {
			return nil, errs.ToAggregate()
		}
		glog.V(5).Infof("%s admission controller loaded with config: %#v", api.PluginName, config)
		return newImagePolicyPlugin(config)
	})
}

type imagePolicyPlugin struct {
	*admission.Handler
	config *api.ImagePolicyConfig
	client client.Interface

	accepter rules.Accepter

	integratedRegistryMatcher integratedRegistryMatcher

	resolveGroupResources []schema.GroupResource

	projectCache *cache.ProjectCache
	resolver     imageResolver
}

var _ = oadmission.WantsOpenshiftClient(&imagePolicyPlugin{})
var _ = oadmission.WantsDefaultRegistryFunc(&imagePolicyPlugin{})

type integratedRegistryMatcher struct {
	rules.RegistryMatcher
}

// imageResolver abstracts identifying an image for a particular reference.
type imageResolver interface {
	ResolveObjectReference(ref *kapi.ObjectReference, defaultNamespace string) (*rules.ImagePolicyAttributes, error)
}

// imagePolicyPlugin returns an admission controller for pods that controls what images are allowed to run on the
// cluster.
func newImagePolicyPlugin(parsed *api.ImagePolicyConfig) (*imagePolicyPlugin, error) {
	m := integratedRegistryMatcher{
		RegistryMatcher: rules.NewRegistryMatcher(nil),
	}
	accepter, err := rules.NewExecutionRulesAccepter(parsed.ExecutionRules, m)
	if err != nil {
		return nil, err
	}

	return &imagePolicyPlugin{
		Handler: admission.NewHandler(admission.Create, admission.Update),
		config:  parsed,

		accepter: accepter,

		integratedRegistryMatcher: m,
	}, nil
}

func (a *imagePolicyPlugin) SetDefaultRegistryFunc(fn imageapi.DefaultRegistryFunc) {
	a.integratedRegistryMatcher.RegistryMatcher = rules.RegistryNameMatcher(fn)
}

func (a *imagePolicyPlugin) SetOpenshiftClient(c client.Interface) {
	a.client = c
}

func (a *imagePolicyPlugin) SetProjectCache(c *cache.ProjectCache) {
	a.projectCache = c
}

// Validate ensures that all required interfaces have been provided, or returns an error.
func (a *imagePolicyPlugin) Validate() error {
	if a.client == nil {
		return fmt.Errorf("%s needs an Openshift client", api.PluginName)
	}
	if a.projectCache == nil {
		return fmt.Errorf("%s needs a project cache", api.PluginName)
	}
	imageResolver, err := newImageResolutionCache(a.client.Images(), a.client, a.client, a.integratedRegistryMatcher)
	if err != nil {
		return fmt.Errorf("unable to create image policy controller: %v", err)
	}
	a.resolver = imageResolver
	return nil
}

// mutateAttributesToLegacyResources mutates the admission attributes in a way where the
// Origin API groups are converted to "legacy" or "core" group.
// This provides a backward compatibility with existing configurations and also closes the
// hole where clients might bypass the admission by using API group endpoint and API group
// resource instead of legacy one.
func mutateAttributesToLegacyResources(attr admission.Attributes) admission.Attributes {
	resource := attr.GetResource()
	if len(resource.Group) > 0 && latest.IsOriginAPIGroup(resource.Group) {
		resource.Group = ""
	}
	kind := attr.GetKind()
	if len(kind.Group) > 0 && latest.IsOriginAPIGroup(kind.Group) {
		kind.Group = ""
	}
	attrs := admission.NewAttributesRecord(
		attr.GetObject(),
		attr.GetOldObject(),
		kind,
		attr.GetNamespace(),
		attr.GetName(),
		resource,
		attr.GetSubresource(),
		attr.GetOperation(),
		attr.GetUserInfo(),
	)
	return attrs
}

// Admit attempts to apply the image policy to the incoming resource.
func (a *imagePolicyPlugin) Admit(attr admission.Attributes) error {
	switch attr.GetOperation() {
	case admission.Create, admission.Update:
		if len(attr.GetSubresource()) > 0 {
			return nil
		}
		// only create and update are tested, and only on core resources
		// TODO: scan all resources
		// TODO: Create a general equivalence map for admission - operation X on subresource Y is equivalent to reduced operation
	default:
		return nil
	}

	newAttr := mutateAttributesToLegacyResources(attr)

	// This will convert any non-legacy Origin resource to a legacy resource, so specifying
	// a 'builds.build.openshift.io' is converted to 'builds'.
	gr := newAttr.GetResource().GroupResource()
	if !a.accepter.Covers(gr) {
		return nil
	}

	m, err := meta.GetImageReferenceMutator(newAttr.GetObject())
	if err != nil {
		return apierrs.NewForbidden(gr, newAttr.GetName(), fmt.Errorf("unable to apply image policy against objects of type %T: %v", newAttr.GetObject(), err))
	}

	// load exclusion rules from the namespace cache
	var excluded sets.String
	if ns := newAttr.GetNamespace(); len(ns) > 0 {
		if ns, err := a.projectCache.GetNamespace(ns); err == nil {
			if value := ns.Annotations[api.IgnorePolicyRulesAnnotation]; len(value) > 0 {
				excluded = sets.NewString(strings.Split(value, ",")...)
			}
		}
	}

	if err := accept(a.accepter, a.config.ResolveImages, a.resolver, m, newAttr, excluded); err != nil {
		return err
	}

	return nil
}

type imageResolutionCache struct {
	images     client.ImageInterface
	tags       client.ImageStreamTagsNamespacer
	isImages   client.ImageStreamImagesNamespacer
	integrated rules.RegistryMatcher
	expiration time.Duration

	cache *lru.Cache
}

type imageCacheEntry struct {
	expires time.Time
	image   *imageapi.Image
}

// newImageResolutionCache creates a new resolver that caches frequently loaded images for one minute.
func newImageResolutionCache(images client.ImageInterface, tags client.ImageStreamTagsNamespacer, isImages client.ImageStreamImagesNamespacer, integratedRegistry rules.RegistryMatcher) (*imageResolutionCache, error) {
	imageCache, err := lru.New(128)
	if err != nil {
		return nil, err
	}
	return &imageResolutionCache{
		images:     images,
		tags:       tags,
		isImages:   isImages,
		integrated: integratedRegistry,
		cache:      imageCache,
		expiration: time.Minute,
	}, nil
}

var now = time.Now

// ResolveObjectReference converts a reference into an image API or returns an error. If the kind is not recognized
// this method will return an error to prevent references that may be images from being ignored.
func (c *imageResolutionCache) ResolveObjectReference(ref *kapi.ObjectReference, defaultNamespace string) (*rules.ImagePolicyAttributes, error) {
	switch ref.Kind {
	case "ImageStreamTag":
		ns := ref.Namespace
		if len(ns) == 0 {
			ns = defaultNamespace
		}
		name, tag, ok := imageapi.SplitImageStreamTag(ref.Name)
		if !ok {
			return &rules.ImagePolicyAttributes{IntegratedRegistry: true}, fmt.Errorf("references of kind ImageStreamTag must be of the form NAME:TAG")
		}
		return c.resolveImageStreamTag(ns, name, tag)

	case "ImageStreamImage":
		ns := ref.Namespace
		if len(ns) == 0 {
			ns = defaultNamespace
		}
		name, id, ok := imageapi.SplitImageStreamImage(ref.Name)
		if !ok {
			return &rules.ImagePolicyAttributes{IntegratedRegistry: true}, fmt.Errorf("references of kind ImageStreamImage must be of the form NAME@DIGEST")
		}
		return c.resolveImageStreamImage(ns, name, id)

	case "DockerImage":
		ref, err := imageapi.ParseDockerImageReference(ref.Name)
		if err != nil {
			return nil, err
		}
		return c.resolveImageReference(ref)

	default:
		return nil, fmt.Errorf("image policy does not allow image references of kind %q", ref.Kind)
	}
}

// Resolve converts an image reference into a resolved image or returns an error. Only images located in the internal
// registry or those with a digest can be resolved - all other scenarios will return an error.
func (c *imageResolutionCache) resolveImageReference(ref imageapi.DockerImageReference) (*rules.ImagePolicyAttributes, error) {
	// images by ID can be checked for policy
	if len(ref.ID) > 0 {
		now := now()
		if value, ok := c.cache.Get(ref.ID); ok {
			cached := value.(imageCacheEntry)
			if now.Before(cached.expires) {
				return &rules.ImagePolicyAttributes{Name: ref, Image: cached.image}, nil
			}
		}
		image, err := c.images.Get(ref.ID, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		c.cache.Add(ref.ID, imageCacheEntry{expires: now.Add(c.expiration), image: image})
		return &rules.ImagePolicyAttributes{Name: ref, Image: image}, nil
	}

	if !c.integrated.Matches(ref.Registry) {
		return nil, fmt.Errorf("only images imported into the registry are allowed (%s)", ref.Exact())
	}

	tag := ref.Tag
	if len(tag) == 0 {
		tag = imageapi.DefaultImageTag
	}

	return c.resolveImageStreamTag(ref.Namespace, ref.Name, tag)
}

// resolveImageStreamTag loads an image stream tag and creates a fully qualified image stream image reference,
// or returns an error.
func (c *imageResolutionCache) resolveImageStreamTag(namespace, name, tag string) (*rules.ImagePolicyAttributes, error) {
	attrs := &rules.ImagePolicyAttributes{IntegratedRegistry: true}
	resolved, err := c.tags.ImageStreamTags(namespace).Get(name, tag)
	if err != nil {
		return attrs, err
	}
	ref, err := imageapi.ParseDockerImageReference(resolved.Image.DockerImageReference)
	if err != nil {
		return attrs, fmt.Errorf("ImageStreamTag could not be resolved: %v", err)
	}
	ref.Tag = ""
	ref.ID = resolved.Image.Name

	now := now()
	c.cache.Add(resolved.Image.Name, imageCacheEntry{expires: now.Add(c.expiration), image: &resolved.Image})

	attrs.Name = ref
	attrs.Image = &resolved.Image
	return attrs, nil
}

// resolveImageStreamImage loads an image stream image if it exists, or returns an error.
func (c *imageResolutionCache) resolveImageStreamImage(namespace, name, id string) (*rules.ImagePolicyAttributes, error) {
	attrs := &rules.ImagePolicyAttributes{IntegratedRegistry: true}
	resolved, err := c.isImages.ImageStreamImages(namespace).Get(name, id)
	if err != nil {
		return attrs, err
	}
	ref, err := imageapi.ParseDockerImageReference(resolved.Image.DockerImageReference)
	if err != nil {
		return attrs, fmt.Errorf("ImageStreamTag could not be resolved: %v", err)
	}
	now := now()
	c.cache.Add(resolved.Image.Name, imageCacheEntry{expires: now.Add(c.expiration), image: &resolved.Image})

	attrs.Name = ref
	attrs.Image = &resolved.Image
	return attrs, nil
}
