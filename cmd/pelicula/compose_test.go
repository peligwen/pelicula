package main

import (
	"slices"
	"testing"
)

func TestBuildArgsIncludesProjectName(t *testing.T) {
	c := NewCompose("/tmp/pelicula", false, false, "pelicula")
	args := c.buildArgs("up", "-d")

	// --project-name and value must appear in args
	idx := slices.Index(args, "--project-name")
	if idx == -1 {
		t.Fatal("buildArgs did not include --project-name")
	}
	if idx+1 >= len(args) || args[idx+1] != "pelicula" {
		t.Errorf("expected --project-name pelicula, got args[%d+1]=%q", idx, args[idx+1])
	}
}

func TestBuildArgsCustomProjectName(t *testing.T) {
	c := NewCompose("/tmp/pelicula", false, false, "my-media-stack")
	args := c.buildArgs("ps")

	idx := slices.Index(args, "--project-name")
	if idx == -1 {
		t.Fatal("buildArgs did not include --project-name")
	}
	if idx+1 >= len(args) || args[idx+1] != "my-media-stack" {
		t.Errorf("expected --project-name my-media-stack, got %q", args[idx+1])
	}
}

func TestNewComposeDefaultsProjectName(t *testing.T) {
	c := NewCompose("/tmp/pelicula", false, false, "")
	if c.projectName != "pelicula" {
		t.Errorf("expected default projectName %q, got %q", "pelicula", c.projectName)
	}
}
