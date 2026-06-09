//go:build darwin || linux || windows

package commands

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func addOSCacheCmd(parent *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Manage cached OS images",
	}

	cmd.AddCommand(newOSCacheListCmd())
	cmd.AddCommand(newOSCacheClearCmd())

	parent.AddCommand(cmd)
}

func newOSCacheListCmd() *cobra.Command {
	type osCacheEntry struct {
		Name      string `json:"name"`
		SizeBytes int64  `json:"sizeBytes"`
		Size      string `json:"size"`
	}

	printJSON := func(items []osCacheEntry) error {
		if items == nil {
			items = []osCacheEntry{}
		}
		data, err := json.MarshalIndent(items, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	return &cobra.Command{
		Use:   "list",
		Short: "List cached OS images",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// jsonOutput is auto-enabled for non-interactive commands; os cache list
			// keeps plain text there unless --json was explicitly requested.
			explicitJSON := jsonOutput && cmd.Root().PersistentFlags().Changed("json")
			dir, err := osCacheDir()
			if err != nil {
				return err
			}

			entries, err := os.ReadDir(dir)
			if err != nil {
				if os.IsNotExist(err) {
					if explicitJSON {
						return printJSON(nil)
					}
					fmt.Println("No cached OS images.")
					return nil
				}
				return fmt.Errorf("reading cache: %w", err)
			}

			var items []osCacheEntry
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				info, err := entry.Info()
				if err != nil {
					return fmt.Errorf("reading OS cache entry info for %s: %w", entry.Name(), err)
				}
				size := info.Size()
				items = append(items, osCacheEntry{
					Name:      entry.Name(),
					SizeBytes: size,
					Size:      formatSize(size),
				})
			}

			if explicitJSON {
				return printJSON(items)
			}

			if len(items) == 0 {
				fmt.Println("No cached OS images.")
			} else {
				for _, item := range items {
					sizeMB := float64(item.SizeBytes) / (1024 * 1024)
					fmt.Printf("  %s  (%.1f MB)\n", item.Name, sizeMB)
				}
				fmt.Printf("\nCache directory: %s\n", dir)
			}

			return nil
		},
	}
}

func newOSCacheClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Clear all cached OS images",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := osCacheDir()
			if err != nil {
				return err
			}

			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("clearing OS image cache: %w", err)
			}

			fmt.Println("OS image cache cleared.")
			return nil
		},
	}
}
