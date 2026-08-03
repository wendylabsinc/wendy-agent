package commands

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/buildspec"
)

type buildSpecOptions struct {
	Dir  string
	File string
}

func compileBuildSpec(opts buildSpecOptions) (buildspec.Result, error) {
	if opts.Dir == "" {
		return buildspec.Result{}, fmt.Errorf("project directory is required")
	}
	filename := opts.File
	if filename == "" {
		filename = buildspec.DefaultFilename
	}
	clean := filepath.Clean(filename)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return buildspec.Result{}, fmt.Errorf("build spec path must stay within the project directory")
	}
	project := os.DirFS(opts.Dir)
	data, err := fs.ReadFile(project, filepath.ToSlash(clean))
	if err != nil {
		return buildspec.Result{}, fmt.Errorf("read %s: %w", clean, err)
	}
	return buildspec.Compile(project, data)
}

func newBuildSpecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "buildspec",
		Short: "Validate or compile Wendy Build Spec",
	}
	cmd.AddCommand(newBuildSpecValidateCmd(), newBuildSpecCompileCmd())
	return cmd
}

func newBuildSpecValidateCmd() *cobra.Command {
	var filename string
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate Wendyfile.toml and print its canonical plan ID",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			result, err := compileBuildSpec(buildSpecOptions{Dir: cwd, File: filename})
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeBuildSpecJSON(cmd, result)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "valid Wendy Build Spec (plan %s)\n", result.Plan.PlanID)
			for _, warning := range result.Plan.Warnings {
				fmt.Fprintf(cmd.OutOrStdout(), "warning: %s\n", warning)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&filename, "file", buildspec.DefaultFilename, "Build spec path relative to the project directory")
	return cmd
}

func newBuildSpecCompileCmd() *cobra.Command {
	var filename, output string
	cmd := &cobra.Command{
		Use:   "compile",
		Short: "Compile Wendyfile.toml into a canonical plan and Dockerfile",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			result, err := compileBuildSpec(buildSpecOptions{Dir: cwd, File: filename})
			if err != nil {
				return err
			}
			if output != "" {
				if err := writeBuildSpecDockerfile(output, []byte(result.Dockerfile)); err != nil {
					return err
				}
			}
			if jsonOutput {
				return writeBuildSpecJSON(cmd, result)
			}
			if output != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "compiled plan %s to %s\n", result.Plan.PlanID, output)
				for _, warning := range result.Plan.Warnings {
					fmt.Fprintf(cmd.OutOrStdout(), "warning: %s\n", warning)
				}
				return nil
			}
			fmt.Fprint(cmd.OutOrStdout(), result.Dockerfile)
			return nil
		},
	}
	cmd.Flags().StringVar(&filename, "file", buildspec.DefaultFilename, "Build spec path relative to the project directory")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Atomically write the generated Dockerfile to this path")
	return cmd
}

func writeBuildSpecJSON(cmd *cobra.Command, result buildspec.Result) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return err
}

func writeBuildSpecDockerfile(filename string, data []byte) error {
	directory := filepath.Dir(filename)
	info, err := os.Stat(directory)
	if err != nil {
		return fmt.Errorf("output directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("output directory %q is not a directory", directory)
	}
	temporary, err := os.CreateTemp(directory, ".wendy-buildspec-*")
	if err != nil {
		return fmt.Errorf("create temporary output: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary output: %w", err)
	}
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return fmt.Errorf("set output mode: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary output: %w", err)
	}
	if err := os.Rename(temporaryName, filename); err != nil {
		return fmt.Errorf("replace %s: %w", filename, err)
	}
	return nil
}
