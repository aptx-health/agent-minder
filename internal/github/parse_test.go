package github

import (
	"testing"
)

func TestParseItemRef_OwnerRepoHash(t *testing.T) {
	ref, err := ParseItemRef("octocat/hello-world#42", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Owner != "octocat" || ref.Repo != "hello-world" || ref.Number != 42 {
		t.Errorf("got %+v", ref)
	}
}

func TestParseItemRef_ShortRefWithDefaults(t *testing.T) {
	ref, err := ParseItemRef("#7", "myorg", "myrepo")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Owner != "myorg" || ref.Repo != "myrepo" || ref.Number != 7 {
		t.Errorf("got %+v", ref)
	}
}

func TestParseItemRef_PlainNumber(t *testing.T) {
	ref, err := ParseItemRef("123", "org", "repo")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Number != 123 {
		t.Errorf("got %+v", ref)
	}
}

func TestParseItemRef_ShortRefNoDefaults(t *testing.T) {
	_, err := ParseItemRef("#42", "", "")
	if err == nil {
		t.Error("expected error for short ref without defaults")
	}
}

func TestParseItemRef_Empty(t *testing.T) {
	_, err := ParseItemRef("", "", "")
	if err == nil {
		t.Error("expected error for empty input")
	}
}

func TestParseItemRef_InvalidNumber(t *testing.T) {
	_, err := ParseItemRef("#abc", "o", "r")
	if err == nil {
		t.Error("expected error for non-numeric ref")
	}
}

func TestParseItemRef_ZeroNumber(t *testing.T) {
	_, err := ParseItemRef("#0", "o", "r")
	if err == nil {
		t.Error("expected error for zero number")
	}
}

func TestParseItemRef_NegativeNumber(t *testing.T) {
	_, err := ParseItemRef("#-1", "o", "r")
	if err == nil {
		t.Error("expected error for negative number")
	}
}

func TestParseItemRef_Whitespace(t *testing.T) {
	ref, err := ParseItemRef("  owner/repo#10  ", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Owner != "owner" || ref.Repo != "repo" || ref.Number != 10 {
		t.Errorf("got %+v", ref)
	}
}

func TestParseItemRef_InvalidFormat(t *testing.T) {
	_, err := ParseItemRef("not-a-ref", "", "")
	if err == nil {
		t.Error("expected error for unrecognized format")
	}
}

func TestParseItemRef_MissingRepo(t *testing.T) {
	_, err := ParseItemRef("owner/#42", "", "")
	if err == nil {
		t.Error("expected error for missing repo in prefix")
	}
}
