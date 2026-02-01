package api

import (
	"sort"
	"strings"

	"github.com/cbrown132/driftd/internal/storage"
)

type stackNode struct {
	Name     string
	Path     string
	RepoName string
	Depth    int
	Children []*stackNode
	Status   *storage.StackStatus
	Total    int
	Drifted  int
	Error    int
	Ok       int
	Open     bool

	children map[string]*stackNode
}

func buildStackTree(repoName string, stacks []storage.StackStatus) []*stackNode {
	root := &stackNode{Depth: -1, RepoName: repoName}

	for i := range stacks {
		stack := &stacks[i]
		parts := strings.Split(stack.Path, "/")
		current := root
		pathParts := make([]string, 0, len(parts))
		for _, part := range parts {
			pathParts = append(pathParts, part)
			child := current.children[part]
			if child == nil {
				child = &stackNode{
					Name:     part,
					Path:     strings.Join(pathParts, "/"),
					RepoName: repoName,
					Depth:    current.Depth + 1,
					Open:     current.Depth+1 < 2,
				}
				if current.children == nil {
					current.children = make(map[string]*stackNode)
				}
				current.children[part] = child
				current.Children = append(current.Children, child)
			}
			current = child
		}
		current.Status = stack
	}

	for _, child := range root.Children {
		tallyStackNode(child)
	}
	orderStackNodes(root.Children)
	return root.Children
}

func tallyStackNode(node *stackNode) {
	if node.Status != nil {
		node.Total = 1
		if node.Status.Error != "" {
			node.Error = 1
		} else if node.Status.Drifted {
			node.Drifted = 1
		} else {
			node.Ok = 1
		}
	}

	for _, child := range node.Children {
		tallyStackNode(child)
		node.Total += child.Total
		node.Drifted += child.Drifted
		node.Error += child.Error
		node.Ok += child.Ok
	}
}

func orderStackNodes(nodes []*stackNode) {
	sort.Slice(nodes, func(i, j int) bool {
		iGroup := len(nodes[i].Children) > 0
		jGroup := len(nodes[j].Children) > 0
		if iGroup != jGroup {
			return iGroup
		}
		return strings.ToLower(nodes[i].Name) < strings.ToLower(nodes[j].Name)
	})

	for _, node := range nodes {
		if len(node.Children) > 0 {
			orderStackNodes(node.Children)
		}
	}
}
