	"context"
	"gopkg.in/src-d/go-git-fixtures.v3"
func (s *SuiteCommit) TestParent(c *C) {
	commit, err := s.Commit.Parent(1)
	c.Assert(err, IsNil)
	c.Assert(commit.Hash.String(), Equals, "a5b8b09e2f8fcb0bb99d3ccb0958157b40890d69")
}

func (s *SuiteCommit) TestParentNotFound(c *C) {
	commit, err := s.Commit.Parent(42)
	c.Assert(err, Equals, ErrParentNotFound)
	c.Assert(commit, IsNil)
}
