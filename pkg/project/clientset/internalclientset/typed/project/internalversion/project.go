package internalversion

import (
	api "github.com/openshift/origin/pkg/project/api"
	scheme "github.com/openshift/origin/pkg/project/clientset/internalclientset/scheme"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	rest "k8s.io/client-go/rest"
)

// ProjectsGetter has a method to return a ProjectResourceInterface.
// A group's client should implement this interface.
type ProjectsGetter interface {
	Projects() ProjectResourceInterface
}

// ProjectResourceInterface has methods to work with ProjectResource resources.
type ProjectResourceInterface interface {
	Create(*api.Project) (*api.Project, error)
	Update(*api.Project) (*api.Project, error)
	UpdateStatus(*api.Project) (*api.Project, error)
	Delete(name string, options *v1.DeleteOptions) error
	DeleteCollection(options *v1.DeleteOptions, listOptions v1.ListOptions) error
	Get(name string, options v1.GetOptions) (*api.Project, error)
	List(opts v1.ListOptions) (*api.ProjectList, error)
	Watch(opts v1.ListOptions) (watch.Interface, error)
	Patch(name string, pt types.PatchType, data []byte, subresources ...string) (result *api.Project, err error)
	ProjectResourceExpansion
}

// projects implements ProjectResourceInterface
type projects struct {
	client rest.Interface
}

// newProjects returns a Projects
func newProjects(c *ProjectClient) *projects {
	return &projects{
		client: c.RESTClient(),
	}
}

// Create takes the representation of a project and creates it.  Returns the server's representation of the project, and an error, if there is any.
func (c *projects) Create(project *api.Project) (result *api.Project, err error) {
	result = &api.Project{}
	err = c.client.Post().
		Resource("projects").
		Body(project).
		Do().
		Into(result)
	return
}

// Update takes the representation of a project and updates it. Returns the server's representation of the project, and an error, if there is any.
func (c *projects) Update(project *api.Project) (result *api.Project, err error) {
	result = &api.Project{}
	err = c.client.Put().
		Resource("projects").
		Name(project.Name).
		Body(project).
		Do().
		Into(result)
	return
}

// UpdateStatus was generated because the type contains a Status member.
// Add a +genclientstatus=false comment above the type to avoid generating UpdateStatus().

func (c *projects) UpdateStatus(project *api.Project) (result *api.Project, err error) {
	result = &api.Project{}
	err = c.client.Put().
		Resource("projects").
		Name(project.Name).
		SubResource("status").
		Body(project).
		Do().
		Into(result)
	return
}

// Delete takes name of the project and deletes it. Returns an error if one occurs.
func (c *projects) Delete(name string, options *v1.DeleteOptions) error {
	return c.client.Delete().
		Resource("projects").
		Name(name).
		Body(options).
		Do().
		Error()
}

// DeleteCollection deletes a collection of objects.
func (c *projects) DeleteCollection(options *v1.DeleteOptions, listOptions v1.ListOptions) error {
	return c.client.Delete().
		Resource("projects").
		VersionedParams(&listOptions, scheme.ParameterCodec).
		Body(options).
		Do().
		Error()
}

// Get takes name of the project, and returns the corresponding project object, and an error if there is any.
func (c *projects) Get(name string, options v1.GetOptions) (result *api.Project, err error) {
	result = &api.Project{}
	err = c.client.Get().
		Resource("projects").
		Name(name).
		VersionedParams(&options, scheme.ParameterCodec).
		Do().
		Into(result)
	return
}

// List takes label and field selectors, and returns the list of Projects that match those selectors.
func (c *projects) List(opts v1.ListOptions) (result *api.ProjectList, err error) {
	result = &api.ProjectList{}
	err = c.client.Get().
		Resource("projects").
		VersionedParams(&opts, scheme.ParameterCodec).
		Do().
		Into(result)
	return
}

// Watch returns a watch.Interface that watches the requested projects.
func (c *projects) Watch(opts v1.ListOptions) (watch.Interface, error) {
	opts.Watch = true
	return c.client.Get().
		Resource("projects").
		VersionedParams(&opts, scheme.ParameterCodec).
		Watch()
}

// Patch applies the patch and returns the patched project.
func (c *projects) Patch(name string, pt types.PatchType, data []byte, subresources ...string) (result *api.Project, err error) {
	result = &api.Project{}
	err = c.client.Patch(pt).
		Resource("projects").
		SubResource(subresources...).
		Name(name).
		Body(data).
		Do().
		Into(result)
	return
}
