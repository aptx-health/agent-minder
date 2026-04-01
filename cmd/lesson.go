package cmd

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aptx-health/agent-minder/internal/claudecli"
	"github.com/aptx-health/agent-minder/internal/db"
	"github.com/aptx-health/agent-minder/internal/lesson"
	"github.com/spf13/cobra"
)

var lessonCmd = &cobra.Command{
	Use:   "lesson",
	Short: "Manage persistent lessons",
}

var lessonAddCmd = &cobra.Command{
	Use:   "add <text>",
	Short: "Add a new lesson",
	Args:  cobra.ExactArgs(1),
	RunE:  runLessonAdd,
}

var lessonListCmd = &cobra.Command{
	Use:   "list",
	Short: "List lessons",
	RunE:  runLessonList,
}

var lessonEditCmd = &cobra.Command{
	Use:   "edit <id> <text>",
	Short: "Edit a lesson",
	Args:  cobra.ExactArgs(2),
	RunE:  runLessonEdit,
}

var lessonRemoveCmd = &cobra.Command{
	Use:   "remove <id>",
	Short: "Remove a lesson",
	Args:  cobra.ExactArgs(1),
	RunE:  runLessonRemove,
}

var lessonPinCmd = &cobra.Command{
	Use:   "pin <id>",
	Short: "Pin a lesson (always injected)",
	Args:  cobra.ExactArgs(1),
	RunE:  runLessonPin,
}

var (
	flagLessonRepo     string
	flagLessonPin      bool
	flagLessonAll      bool
	flagLessonInactive bool
)

var lessonGroomCmd = &cobra.Command{
	Use:   "groom",
	Short: "LLM-assisted lesson consolidation",
	RunE:  runLessonGroom,
}

var flagGroomDryRun bool

func init() {
	rootCmd.AddCommand(lessonCmd)
	lessonCmd.AddCommand(lessonAddCmd, lessonListCmd, lessonEditCmd, lessonRemoveCmd, lessonPinCmd, lessonGroomCmd)
	lessonGroomCmd.Flags().BoolVar(&flagGroomDryRun, "dry-run", false, "Show what would change without applying")

	lessonAddCmd.Flags().StringVar(&flagLessonRepo, "repo", "", "Scope to repo (owner/repo)")
	lessonAddCmd.Flags().BoolVar(&flagLessonPin, "pin", false, "Pin this lesson")
	lessonListCmd.Flags().StringVar(&flagLessonRepo, "repo", "", "Filter by repo scope")
	lessonListCmd.Flags().BoolVar(&flagLessonAll, "all", false, "Include all scopes")
	lessonListCmd.Flags().BoolVar(&flagLessonInactive, "inactive", false, "Include inactive lessons")
}

func openStore() (*db.Store, error) {
	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return nil, err
	}
	return db.NewStore(conn), nil
}

func runLessonAdd(cmd *cobra.Command, args []string) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	l := &db.Lesson{
		Content: args[0],
		Source:  "manual",
		Active:  true,
		Pinned:  flagLessonPin,
	}
	if flagLessonRepo != "" {
		l.RepoScope = sql.NullString{String: flagLessonRepo, Valid: true}
	}

	if err := store.CreateLesson(l); err != nil {
		return err
	}

	scope := "global"
	if flagLessonRepo != "" {
		scope = flagLessonRepo
	}
	pin := ""
	if flagLessonPin {
		pin = " [pinned]"
	}
	fmt.Printf("Lesson #%d added (%s)%s\n", l.ID, scope, pin)
	return nil
}

func runLessonList(cmd *cobra.Command, args []string) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	lessons, err := store.GetAllLessons(flagLessonRepo, flagLessonInactive)
	if err != nil {
		return err
	}

	if len(lessons) == 0 {
		fmt.Println("No lessons found.")
		return nil
	}

	for _, l := range lessons {
		scope := "global"
		if l.RepoScope.Valid {
			scope = l.RepoScope.String
		}
		status := ""
		if !l.Active {
			status = " [inactive]"
		}
		pin := ""
		if l.Pinned {
			pin = " [pinned]"
		}
		stats := fmt.Sprintf("inj:%d +%d -%d", l.TimesInjected, l.TimesHelpful, l.TimesUnhelpful)
		fmt.Printf("#%-4d %-8s %-6s%s%s  %s\n", l.ID, scope, stats, pin, status, truncateStr(l.Content, 60))
	}
	return nil
}

func runLessonEdit(cmd *cobra.Command, args []string) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("invalid lesson ID: %s", args[0])
	}

	if err := store.UpdateLessonContent(id, args[1]); err != nil {
		return err
	}
	fmt.Printf("Lesson #%d updated.\n", id)
	return nil
}

func runLessonRemove(cmd *cobra.Command, args []string) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("invalid lesson ID: %s", args[0])
	}

	if err := store.DeleteLesson(id); err != nil {
		return err
	}
	fmt.Printf("Lesson #%d removed.\n", id)
	return nil
}

func runLessonPin(cmd *cobra.Command, args []string) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("invalid lesson ID: %s", args[0])
	}

	l, err := store.GetLesson(id)
	if err != nil {
		return fmt.Errorf("lesson #%d not found", id)
	}

	newPinned := !l.Pinned
	if err := store.UpdateLessonPinned(id, newPinned); err != nil {
		return err
	}
	if newPinned {
		fmt.Printf("Lesson #%d pinned.\n", id)
	} else {
		fmt.Printf("Lesson #%d unpinned.\n", id)
	}
	return nil
}

func runLessonGroom(cmd *cobra.Command, args []string) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	// Step 1: Deactivate stale lessons (90 days).
	stale, err := lesson.GroomStale(store, 90*24*time.Hour)
	if err != nil {
		return fmt.Errorf("groom stale: %w", err)
	}
	if stale > 0 {
		fmt.Printf("Deactivated %d stale lessons (>90 days without injection)\n", stale)
	}

	// Step 2: Deactivate ineffective lessons (>5 injections, more unhelpful than helpful).
	ineffective, err := lesson.GroomIneffective(store, 5)
	if err != nil {
		return fmt.Errorf("groom ineffective: %w", err)
	}
	if ineffective > 0 {
		fmt.Printf("Deactivated %d ineffective lessons\n", ineffective)
	}

	// Step 3: LLM-assisted consolidation.
	completer := claudecli.NewCLICompleter()
	result, err := lesson.GroomWithLLM(context.Background(), store, completer, flagGroomDryRun)
	if err != nil {
		return fmt.Errorf("LLM groom: %w", err)
	}

	if flagGroomDryRun {
		fmt.Printf("\n[dry-run] %d actions would be taken\n", result.Flagged)
	} else {
		fmt.Printf("LLM groom: %d merged, %d deactivated\n", result.Merged, result.Deactivated)
	}

	return nil
}

func truncateStr(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
