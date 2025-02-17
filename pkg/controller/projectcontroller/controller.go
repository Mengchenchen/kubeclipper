/*
 *
 *  * Copyright 2021 KubeClipper Authors.
 *  *
 *  * Licensed under the Apache License, Version 2.0 (the "License");
 *  * you may not use this file except in compliance with the License.
 *  * You may obtain a copy of the License at
 *  *
 *  *     http://www.apache.org/licenses/LICENSE-2.0
 *  *
 *  * Unless required by applicable law or agreed to in writing, software
 *  * distributed under the License is distributed on an "AS IS" BASIS,
 *  * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 *  * See the License for the specific language governing permissions and
 *  * limitations under the License.
 *
 */

// Package projectcontroller implements controller of project.
package projectcontroller

import (
	"context"
	"fmt"
	"reflect"

	pkgerrors "github.com/pkg/errors"
	"go.uber.org/zap"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubeclipper/kubeclipper/pkg/controller-runtime/client"
	"github.com/kubeclipper/kubeclipper/pkg/controller-runtime/reconcile"

	"github.com/kubeclipper/kubeclipper/pkg/models/iam"
	"github.com/kubeclipper/kubeclipper/pkg/query"
	iamv1 "github.com/kubeclipper/kubeclipper/pkg/scheme/iam/v1"

	"github.com/kubeclipper/kubeclipper/pkg/models/tenant"

	corev1lister "github.com/kubeclipper/kubeclipper/pkg/client/lister/core/v1"
	"github.com/kubeclipper/kubeclipper/pkg/controller-runtime/handler"
	"github.com/kubeclipper/kubeclipper/pkg/controller-runtime/source"
	"github.com/kubeclipper/kubeclipper/pkg/models/cluster"
	"github.com/kubeclipper/kubeclipper/pkg/scheme/common"
	v1 "github.com/kubeclipper/kubeclipper/pkg/scheme/core/v1"

	"github.com/kubeclipper/kubeclipper/pkg/client/informers"
	tenantv1Lister "github.com/kubeclipper/kubeclipper/pkg/client/lister/tenant/v1"
	"github.com/kubeclipper/kubeclipper/pkg/controller-runtime/controller"
	"github.com/kubeclipper/kubeclipper/pkg/controller-runtime/manager"
	tenantv1 "github.com/kubeclipper/kubeclipper/pkg/scheme/tenant/v1"

	ctrl "github.com/kubeclipper/kubeclipper/pkg/controller-runtime"
	"github.com/kubeclipper/kubeclipper/pkg/logger"
)

// ProjectReconciler .
type ProjectReconciler struct {
	ProjectLister tenantv1Lister.ProjectLister
	ProjectWriter tenant.ProjectWriter

	NodeLister corev1lister.NodeLister
	NodeWriter cluster.NodeWriter

	ClusterLister corev1lister.ClusterLister
	IAMOperator   iam.Operator

	// ProjectRoleReader iam.ProjectRoleReader
	// ProjectRoleWriter iam.ProjectRoleWriter
	// ProjectRoleBindingReader iam.ProjectRoleBindingReader
	// ProjectRoleBindingWrite  iam.ProjectRoleBindingWriter
}

// SetupWithManager add controller to mgr.
func (r *ProjectReconciler) SetupWithManager(mgr manager.Manager, cache informers.InformerCache) error {
	c, err := controller.NewUnmanaged("project", controller.Options{
		MaxConcurrentReconciles: 1, // must run serialize
		Reconciler:              r,
		Log:                     mgr.GetLogger().WithName("project-controller"),
		RecoverPanic:            true,
	})
	if err != nil {
		return err
	}

	if err = c.Watch(source.NewKindWithCache(&tenantv1.Project{}, cache), &handler.EnqueueRequestForObject{}); err != nil {
		return err
	}
	if err = c.Watch(source.NewKindWithCache(&v1.Cluster{}, cache), handler.EnqueueRequestsFromMapFunc(r.findObjectsFromCluster)); err != nil {
		return err
	}
	if err = c.Watch(source.NewKindWithCache(&v1.Node{}, cache), handler.EnqueueRequestsFromMapFunc(r.findObjectsFromNode)); err != nil {
		return err
	}

	mgr.AddRunnable(c)
	return nil
}

// findObjectsFromCluster when cluster changed,we need sync cluster's node.
func (r *ProjectReconciler) findObjectsFromCluster(clu client.Object) []reconcile.Request {
	projectName := clu.GetLabels()[common.LabelProject]
	return []reconcile.Request{
		{NamespacedName: types.NamespacedName{
			Name: projectName,
		}},
	}
}

func (r *ProjectReconciler) findObjectsFromNode(clu client.Object) []reconcile.Request {
	// just watch delete event
	if clu.GetDeletionTimestamp().IsZero() {
		return []reconcile.Request{}
	}
	project := clu.GetLabels()[common.LabelProject]
	// just watch node which joined project
	if project == "" {
		return []reconcile.Request{}
	}
	return []reconcile.Request{
		{NamespacedName: types.NamespacedName{
			Name: project,
		}},
	}
}

func (r *ProjectReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logger.FromContext(ctx)

	project, err := r.ProjectLister.Get(req.Name)
	if err != nil {
		// project not found, possibly been deleted
		// need to do the cleanup
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error("failed to get project", zap.Error(err))
		return ctrl.Result{}, err
	}

	if project.ObjectMeta.DeletionTimestamp.IsZero() {
		// insert finalizers
		if !sets.NewString(project.ObjectMeta.Finalizers...).Has(v1.ProjectFinalizer) {
			project.ObjectMeta.Finalizers = append(project.ObjectMeta.Finalizers, v1.ProjectFinalizer)
		}
		project, err = r.ProjectWriter.UpdateProject(context.TODO(), project)
		if err != nil {
			log.Error("update project,add finalizer", zap.Error(err))
			return ctrl.Result{}, err
		}
	} else {
		// The object is being deleted
		if sets.NewString(project.ObjectMeta.Finalizers...).Has(v1.ProjectFinalizer) {
			// delete projectRole and RoleBinding
			if err = r.deleteRoleAndRoleBinding(ctx, project); err != nil {
				log.Error("failed to delete role roleBinding", zap.Error(err))
				return ctrl.Result{}, err
			}

			// when cleanup remove our project finalizer
			finalizers := sets.NewString(project.ObjectMeta.Finalizers...)
			finalizers.Delete(v1.ProjectFinalizer)
			project.ObjectMeta.Finalizers = finalizers.List()
			if _, err = r.ProjectWriter.UpdateProject(ctx, project); err != nil {
				log.Error("failed to delete finalizer", zap.Error(err))
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if err = r.initProjectRole(ctx, project); err != nil {
		log.Error("failed to init project role", zap.Error(err))
		return ctrl.Result{}, err
	}

	if err = r.initManagerRoleBinding(ctx, project); err != nil {
		log.Error("failed to init manager role binding", zap.Error(err))
		return ctrl.Result{}, err
	}

	if err = r.syncProjectNodeFromCluster(ctx, project); err != nil {
		log.Error("failed to sync cluster node", zap.Error(err))
		return ctrl.Result{}, err
	}

	if err = r.syncNodeLabel(ctx, project); err != nil {
		log.Error("failed to sync node label", zap.Error(err))
		return ctrl.Result{}, err
	}

	if err = r.count(ctx, project); err != nil {
		log.Error("failed to count project", zap.Error(err))
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// syncProjectNodeFromCluster add all cluster's node to project.
func (r *ProjectReconciler) syncProjectNodeFromCluster(ctx context.Context, p *tenantv1.Project) error {
	log := logger.FromContext(ctx)
	log.Info("syncProjectNodeFromCluster")

	requirement, err := labels.NewRequirement(common.LabelProject, selection.Equals, []string{p.Name})
	if err != nil {
		return err
	}
	clusters, err := r.ClusterLister.List(labels.NewSelector().Add(*requirement))
	if err != nil {
		return err
	}

	set := sets.NewString()
	for _, clu := range clusters {
		set.Insert(clu.GetAllNodes().List()...)
	}

	if !set.Equal(sets.NewString(p.Spec.Nodes...)) {
		p.Spec.Nodes = set.List()
		if _, err = r.ProjectWriter.UpdateProject(ctx, p); err != nil {
			return err
		}
	}

	return nil
}

func (r *ProjectReconciler) initProjectRole(ctx context.Context, p *tenantv1.Project) error {
	log := logger.FromContext(ctx)
	log.Info("initProjectRole")

	// check if all base role exist,if not creat it
	for _, rule := range ProjectRoles {
		// rule := ru
		if rule.Labels == nil {
			rule.Labels = make(map[string]string)
		}
		rule.Labels[common.LabelProject] = p.Name
		rule.Name = fmt.Sprintf("%s-%s", p.Name, rule.Name)
		projectRole, err := r.IAMOperator.GetProjectRole(ctx, rule.Name)
		if err != nil {
			if errors.IsNotFound(err) {
				if _, err = r.IAMOperator.CreateProjectRole(ctx, &rule); err != nil {
					return err
				}
				continue
			}
			return err
		}
		// 	if role exist,do equal check
		if !reflect.DeepEqual(rule.Labels, projectRole.Labels) ||
			!reflect.DeepEqual(rule.Annotations, projectRole.Annotations) ||
			!reflect.DeepEqual(rule.Rules, projectRole.Rules) {

			if _, err = r.IAMOperator.UpdateProjectRole(ctx, &rule); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *ProjectReconciler) initManagerRoleBinding(ctx context.Context, p *tenantv1.Project) error {
	log := logger.FromContext(ctx)
	log.Info("initManagerRoleBinding")

	// bind admin role to project manager
	// check if projectRoleBinding for project.manager exist,if not creat it
	// ManagerRoleBinding use a fixed name({project}-{username}),if project.manager changed,we just update it.
	// name := fmt.Sprintf("internal-%s-manager", p.Name)
	target := &iamv1.ProjectRoleBinding{
		TypeMeta: metav1.TypeMeta{
			Kind:       iamv1.KindProjectRoleBinding,
			APIVersion: iamv1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-%s", p.Name, p.Spec.Manager),
			Annotations: map[string]string{
				"kubeclipper.io/internal": "true",
			},
			Labels: map[string]string{
				common.LabelProject:                   p.Name,
				common.LabelProjectRoleBindingManager: "true",
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "iam.kubeclipper.io",
			Kind:     iamv1.KindProjectRole,
			Name:     fmt.Sprintf("%s-admin", p.Name),
		},
		Subjects: []rbacv1.Subject{
			{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     iamv1.KindUser,
				Name:     p.Spec.Manager,
			},
		},
	}

	q := query.New()
	q.AddLabelSelector([]string{fmt.Sprintf("%s=%s", common.LabelProject, p.Name), fmt.Sprintf("%s=%s", common.LabelProjectRoleBindingManager, "true")})
	roleBinding, err := r.IAMOperator.ListProjectRoleBinding(ctx, q)
	if err != nil {
		return err
	}
	// not exist,creat it
	if len(roleBinding.Items) == 0 {
		if _, err = r.IAMOperator.CreateProjectRoleBinding(ctx, target); err != nil {
			return err
		}
		return nil
	}
	// if exist,check subject name is ok
	// in normal,there just one robeBinding with label common.LabelProjectRoleBindingManager
	for _, item := range roleBinding.Items {
		if len(item.Subjects) == 0 || item.Subjects[0].Name != p.Spec.Manager {
			name := fmt.Sprintf("%s-%s", p.Name, item.Subjects[0].Name)
			// delete old manager's projectRoleBinding
			if err = r.IAMOperator.DeleteProjectRoleBinding(ctx, item.Name); err != nil && !errors.IsNotFound(err) {
				return err
			}
			// delete old manager's global view roleBinding
			if err = r.IAMOperator.DeleteRoleBinding(ctx, name); err != nil && !errors.IsNotFound(err) {
				return err
			}
			if _, err = r.IAMOperator.CreateProjectRoleBinding(ctx, target); err != nil {
				return err
			}
		}
	}

	return nil
}

func (r *ProjectReconciler) deleteRoleAndRoleBinding(ctx context.Context, p *tenantv1.Project) error {
	log := logger.FromContext(ctx)
	log.Info("deleteRoleAndRoleBinding")
	// delete projectRole and projectRoleBinding which in this project

	q := query.New()
	q.LabelSelector = fmt.Sprintf("%s-%s", common.LabelProject, p.Name)
	roles, err := r.IAMOperator.ListProjectRoles(ctx, q)
	if err != nil {
		return err
	}
	for _, role := range roles.Items {
		if err = r.IAMOperator.DeleteProjectRole(ctx, role.Name); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}

	roleBindings, err := r.IAMOperator.ListProjectRoleBinding(ctx, q)
	if err != nil {
		return err
	}
	for _, roleBinding := range roleBindings.Items {
		if err = r.IAMOperator.DeleteProjectRoleBinding(ctx, roleBinding.Name); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}

	name := fmt.Sprintf("%s-%s", p.Name, p.Spec.Manager)
	if err = r.IAMOperator.DeleteRoleBinding(ctx, name); err != nil && !errors.IsNotFound(err) {
		return err
	}
	return nil
}

// syncNodeLabel sync node's label by spec.nodes
func (r *ProjectReconciler) syncNodeLabel(ctx context.Context, p *tenantv1.Project) error {
	// list nodes in this project
	requirement, err := labels.NewRequirement(common.LabelProject, selection.Equals, []string{p.Name})
	if err != nil {
		return err
	}
	list, err := r.NodeLister.List(labels.NewSelector().Add(*requirement))
	if err != nil {
		return err
	}
	// remove label when node leave project
	for _, node := range list {
		set := sets.NewString(p.Spec.Nodes...)
		if !set.Has(node.Name) {
			delete(node.Labels, common.LabelProject)
			if _, err = r.NodeWriter.UpdateNode(ctx, node); err != nil {
				return pkgerrors.WithMessagef(err, "delete node %s's label", node.Name)
			}
		}
	}
	// add label when node join project
	for _, nodeID := range p.Spec.Nodes {
		node, err := r.NodeLister.Get(nodeID)
		if err != nil {
			return err
		}
		if node.Labels[common.LabelProject] != p.Name {
			node.Labels[common.LabelProject] = p.Name
			if _, err = r.NodeWriter.UpdateNode(ctx, node); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *ProjectReconciler) count(ctx context.Context, p *tenantv1.Project) error {
	requirement, err := labels.NewRequirement(common.LabelProject, selection.Equals, []string{p.Name})
	if err != nil {
		return err
	}
	selector := labels.NewSelector().Add(*requirement)
	cp := p.DeepCopy()

	// 	count cluster
	clusters, err := r.ClusterLister.List(selector)
	if err != nil {
		return err
	}
	cp.Status.Count.Cluster = int64(len(clusters))
	// 	count node
	cp.Status.Count.Node = int64(len(p.Spec.Nodes))

	if !reflect.DeepEqual(p, cp) {
		if _, err = r.ProjectWriter.UpdateProject(ctx, cp); err != nil {
			return err
		}
	}
	return nil
}

// ProjectRoles project role template
var ProjectRoles = []iamv1.ProjectRole{
	{
		TypeMeta: metav1.TypeMeta{
			Kind:       iamv1.KindProjectRole,
			APIVersion: iamv1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"kubeclipper.io/aggregation-roles": "[\"role-template-view-users\",\"role-template-view-dns\",\"role-template-view-backuppoints\",\"role-template-view-templates\",\"role-template-view-registries\",\"role-template-view-projectroles\",\"role-template-create-projectroles\",\"role-template-edit-projectroles\",\"role-template-delete-projectroles\",\"role-template-view-projectmembers\",\"role-template-create-projectmembers\",\"role-template-edit-projectmembers\",\"role-template-delete-projectmembers\",\"role-template-view-clusters\",\"role-template-access-clusters\",\"role-template-create-clusters\",\"role-template-edit-clusters\",\"role-template-delete-clusters\",\"role-template-view-nodes\",\"role-template-create-nodes\",\"role-template-edit-nodes\",\"role-template-delete-nodes\"]",
				"kubeclipper.io/internal":          "true",
			},
			Name: "admin", // real name will generate when creating,format is {project}-admin
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"*"},
				Resources: []string{"*"},
				Verbs:     []string{"*"},
			},
			{
				NonResourceURLs: []string{"*"},
				Verbs:           []string{"*"},
			},
		},
	},
	{
		TypeMeta: metav1.TypeMeta{
			Kind:       iamv1.KindProjectRole,
			APIVersion: iamv1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"kubeclipper.io/aggregation-roles": "[\"role-template-view-dns\",\"role-template-view-backuppoints\",\"role-template-view-templates\",\"role-template-view-registries\",\"role-template-view-clusters\",\"role-template-access-clusters\",\"role-template-create-clusters\",\"role-template-edit-clusters\",\"role-template-delete-clusters\",\"role-template-view-nodes\",\"role-template-create-nodes\",\"role-template-edit-nodes\",\"role-template-delete-nodes\"]",
				"kubeclipper.io/internal":          "true",
			},
			Name: "user",
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"core.kubeclipper.io"},
				Resources: []string{"domains", "domains/records", "backuppoints", "templates", "templates"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"core.kubeclipper.io"},
				Resources: []string{"clusters", "clusters/plugins", "clusters/join", "clusters/nodes", "clusters/backups", "clusters/cronbackups", "clusters/certification", "clusters/kubeconfig", "nodes", "operations"},
				Verbs:     []string{"*"},
			},
		},
	},
	{
		TypeMeta: metav1.TypeMeta{
			Kind:       iamv1.KindProjectRole,
			APIVersion: iamv1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"kubeclipper.io/aggregation-roles": "[\"role-template-view-dns\",\"role-template-view-backuppoints\",\"role-template-view-templates\",\"role-template-view-registries\",\"role-template-view-projectroles\",\"role-template-view-projectmembers\",\"role-template-view-clusters\",\"role-template-view-nodes\"]",
				"kubeclipper.io/internal":          "true",
			},
			Name: "view",
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"*"},
				Resources: []string{"*"},
				Verbs:     []string{"get", "list", "watch"},
			},
		},
	},
}
