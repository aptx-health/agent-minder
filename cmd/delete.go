package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/spf13/cobra"
)

var deleteForce bool

var deleteCmd = &cobra.Command{
	Use:     "delete <project>",
	Aliases: []string{"rm"},
	Short:   "Delete a project and all its data",
	Args:    cobra.ExactArgs(1),
	RunE:    runDelete,
}

func init() {
	deleteCmd.Flags().BoolVarP(&deleteForce, "force", "f", false, "Skip confirmation prompt")
	rootCmd.AddCommand(deleteCmd)
}

func runDelete(cmd *cobra.Command, args []string) error {
	projectName := args[0]

	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer conn.Close()
	store := db.NewStore(conn)

	project, err := store.GetProject(projectName)
	if err != nil {
		return fmt.Errorf("project %q not found", projectName)
	}

	if !deleteForce {
		reader := bufio.NewReader(os.Stdin)
		fmt.Printf("Delete project %q and all its data? [y/N]: ", projectName)
		answer, _ := reader.ReadString('\n')
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "y") {
			fmt.Println("Aborted.")
			return nil
		}
	}

	if err := store.DeleteProject(project.ID); err != nil {
		return fmt.Errorf("deleting project: %w", err)
	}

	fmt.Printf("Deleted project %q.\n", projectName)
	return nil
}
