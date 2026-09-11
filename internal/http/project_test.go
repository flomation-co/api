package http

import (
	"testing"

	"flomation.app/automate/api"
	. "github.com/onsi/gomega"
)

func strptr(s string) *string { return &s }

// findChild returns the direct child of p with the given id, or nil.
func findChild(p *api.Project, id string) *api.Project {
	for _, c := range p.Children {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func TestBuildProjectTree_NestsChildrenUnderParents(t *testing.T) {
	RegisterTestingT(t)

	flat := []*api.Project{
		{ID: "root-a"},
		{ID: "root-b"},
		{ID: "child-a1", ParentID: strptr("root-a")},
		{ID: "child-a2", ParentID: strptr("root-a")},
		{ID: "grandchild", ParentID: strptr("child-a1")},
	}

	roots := buildProjectTree(flat)

	Expect(roots).To(HaveLen(2))

	var rootA *api.Project
	for _, r := range roots {
		if r.ID == "root-a" {
			rootA = r
		}
	}
	Expect(rootA).ToNot(BeNil())
	Expect(rootA.Children).To(HaveLen(2))

	a1 := findChild(rootA, "child-a1")
	Expect(a1).ToNot(BeNil())
	Expect(a1.Children).To(HaveLen(1))
	Expect(a1.Children[0].ID).To(Equal("grandchild"))
}

func TestBuildProjectTree_OrphanIsTreatedAsRoot(t *testing.T) {
	RegisterTestingT(t)

	// child-x's parent is not in the set (e.g. filtered out by scope). It must
	// surface as a root rather than being silently dropped.
	flat := []*api.Project{
		{ID: "root"},
		{ID: "child-x", ParentID: strptr("missing-parent")},
	}

	roots := buildProjectTree(flat)

	ids := map[string]bool{}
	for _, r := range roots {
		ids[r.ID] = true
	}
	Expect(ids["root"]).To(BeTrue())
	Expect(ids["child-x"]).To(BeTrue())
	Expect(roots).To(HaveLen(2))
}

func TestBuildProjectTree_Empty(t *testing.T) {
	RegisterTestingT(t)
	Expect(buildProjectTree(nil)).To(BeEmpty())
}
