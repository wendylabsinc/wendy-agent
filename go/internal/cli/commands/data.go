package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

func newDataCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "data", Short: "Record and retrieve synchronized device data"}
	cmd.AddCommand(newDataSourcesCmd(), newDataRecordCmd(), newDataStopCmd(), newDataEpisodesCmd(), newDataInspectCmd(), newDataDownloadCmd())
	return cmd
}

func withDataClient(ctx context.Context, fn func(agentpbv2.DataServiceClient) error) error {
	conn, err := connectToAgent(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return fn(conn.DataService)
}

func newDataSourcesCmd() *cobra.Command {
	return &cobra.Command{Use: "sources", Short: "List recordable sources", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return withDataClient(cmd.Context(), func(c agentpbv2.DataServiceClient) error {
			r, e := c.Sources(cmd.Context(), &agentpbv2.DataSourcesRequest{})
			if e != nil {
				return e
			}
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(r)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "SOURCE\tKIND\tCLOCK\tSTATUS")
			for _, s := range r.GetSources() {
				health := "unhealthy"
				if s.GetHealthy() {
					health = "healthy"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", s.GetId(), s.GetKind(), s.GetClockDomain(), health)
			}
			return nil
		})
	}}
}

func newDataRecordCmd() *cobra.Command {
	var name string
	var sources, exclude, calibration []string
	var require time.Duration
	c := &cobra.Command{Use: "record", Short: "Start a detached device-local episode", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var cals []*agentpbv2.DataCalibration
		for _, v := range calibration {
			source, path, ok := strings.Cut(v, "=")
			if !ok || source == "" || path == "" {
				return fmt.Errorf("invalid --calibration %q; want source=local-path", v)
			}
			b, e := os.ReadFile(path)
			if e != nil {
				return fmt.Errorf("reading calibration %s: %w", path, e)
			}
			cals = append(cals, &agentpbv2.DataCalibration{Source: source, Contents: b, Filename: filepath.Base(path)})
		}
		return withDataClient(cmd.Context(), func(client agentpbv2.DataServiceClient) error {
			e, e2 := client.Start(cmd.Context(), &agentpbv2.DataStartRequest{Name: name, Sources: sources, ExcludeSources: exclude, RequireUtcUncertaintyNanos: require.Nanoseconds(), Calibrations: cals})
			if e2 != nil {
				return e2
			}
			return printEpisode(cmd, e)
		})
	}}
	c.Flags().StringVar(&name, "name", "", "Human-readable episode name")
	c.Flags().StringArrayVar(&sources, "source", nil, "Source to record (repeatable)")
	c.Flags().StringArrayVar(&exclude, "exclude-source", nil, "Default source to exclude (repeatable)")
	c.Flags().DurationVar(&require, "require-utc-uncertainty", 0, "Fail unless fresh UTC uncertainty is within this bound")
	c.Flags().StringArrayVar(&calibration, "calibration", nil, "Attach source calibration as source=local-path (repeatable)")
	return c
}

func newDataStopCmd() *cobra.Command {
	return &cobra.Command{Use: "stop", Short: "Stop and seal the active episode", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return withDataClient(cmd.Context(), func(c agentpbv2.DataServiceClient) error {
			e, err := c.Stop(cmd.Context(), &agentpbv2.DataStopRequest{})
			if err != nil {
				return err
			}
			return printEpisode(cmd, e)
		})
	}}
}

func newDataEpisodesCmd() *cobra.Command {
	return &cobra.Command{Use: "episodes", Short: "List finalized episodes", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return withDataClient(cmd.Context(), func(c agentpbv2.DataServiceClient) error {
			r, e := c.Episodes(cmd.Context(), &agentpbv2.DataEpisodesRequest{})
			if e != nil {
				return e
			}
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(r)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "EPISODE\tSTATE\tSIZE\tNAME")
			for _, x := range r.GetEpisodes() {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%d\t%s\n", x.GetId(), x.GetState(), x.GetSizeBytes(), x.GetName())
			}
			return nil
		})
	}}
}

func newDataInspectCmd() *cobra.Command {
	return &cobra.Command{Use: "inspect <episode>", Short: "Inspect and verify an episode manifest", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return withDataClient(cmd.Context(), func(c agentpbv2.DataServiceClient) error {
			r, e := c.Inspect(cmd.Context(), &agentpbv2.DataInspectRequest{Episode: args[0], Verify: true})
			if e != nil {
				return e
			}
			if jsonOutput {
				if _, e = cmd.OutOrStdout().Write(r.GetManifestJson()); e != nil {
					return e
				}
			} else {
				if e = printInspection(cmd, r.GetManifestJson()); e != nil {
					return e
				}
			}
			if len(r.GetVerificationErrors()) > 0 {
				return fmt.Errorf("episode verification failed: %s", strings.Join(r.GetVerificationErrors(), "; "))
			}
			return nil
		})
	}}
}

type inspectionManifest struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	State             string `json:"state"`
	CanonicalClock    string `json:"canonical_clock"`
	BootID            string `json:"boot_id"`
	SystemClockStatus string `json:"system_clock_status"`
	UTC               []struct {
		OffsetLower int64  `json:"offset_lower_nanos"`
		OffsetUpper int64  `json:"offset_upper_nanos"`
		Uncertainty int64  `json:"uncertainty_nanos"`
		Confidence  string `json:"confidence"`
	} `json:"utc_observations"`
	Roughtime []struct {
		Confidence string            `json:"confidence"`
		Quorum     int               `json:"quorum"`
		Evidence   []json.RawMessage `json:"evidence"`
	} `json:"roughtime_observations"`
	Sources []struct {
		Source struct {
			ID          string `json:"id"`
			ClockDomain string `json:"clock_domain"`
		} `json:"source"`
		Count           uint64  `json:"count"`
		Drops           *uint64 `json:"drops"`
		DropAccounting  string  `json:"drop_accounting"`
		MappingError    *int64  `json:"mapping_error_nanos"`
		Discontinuities uint64  `json:"discontinuities"`
	} `json:"sources"`
}

func printInspection(cmd *cobra.Command, b []byte) error {
	var m inspectionManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "Episode: %s", m.ID)
	if m.Name != "" {
		fmt.Fprintf(w, " (%s)", m.Name)
	}
	fmt.Fprintf(w, "\nState: %s\nCanonical clock: %s\nBoot ID: %s\nSystem clock: %s\n", m.State, m.CanonicalClock, m.BootID, m.SystemClockStatus)
	for i, o := range m.UTC {
		label := "start"
		if i == len(m.UTC)-1 && i > 0 {
			label = "end"
		}
		fmt.Fprintf(w, "UTC %s: offset [%d, %d] ns; uncertainty %s; %s\n", label, o.OffsetLower, o.OffsetUpper, time.Duration(o.Uncertainty), o.Confidence)
	}
	for i, o := range m.Roughtime {
		label := "start"
		if i == len(m.Roughtime)-1 && i > 0 {
			label = "end"
		}
		fmt.Fprintf(w, "Roughtime %s: %s; quorum %d; proofs %d\n", label, o.Confidence, o.Quorum, len(o.Evidence))
	}
	if len(m.Sources) > 0 {
		fmt.Fprintln(w, "\nSOURCE\tCLOCK\tCOUNT\tDROPS\tMAPPING ERROR\tDISCONTINUITIES")
		for _, s := range m.Sources {
			drops := "unknown (" + s.DropAccounting + ")"
			if s.Drops != nil {
				if strings.HasPrefix(s.DropAccounting, "exact") {
					drops = fmt.Sprint(*s.Drops)
				} else {
					drops = fmt.Sprintf("%d known (%s)", *s.Drops, s.DropAccounting)
				}
			}
			mapping := "unknown"
			if s.MappingError != nil {
				mapping = time.Duration(*s.MappingError).String()
			}
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%d\n", s.Source.ID, s.Source.ClockDomain, s.Count, drops, mapping, s.Discontinuities)
		}
	}
	return nil
}

type downloadManifest struct {
	Files []struct {
		Path   string `json:"path"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

func newDataDownloadCmd() *cobra.Command {
	var output string
	c := &cobra.Command{Use: "download <episode>", Short: "Resume and verify an episode download", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		if output == "" {
			output = id
		}
		stage := output + ".partial"
		if _, e := os.Stat(output); e == nil {
			return fmt.Errorf("destination already exists: %s", output)
		}
		return withDataClient(cmd.Context(), func(client agentpbv2.DataServiceClient) error {
			inspect, e := client.Inspect(cmd.Context(), &agentpbv2.DataInspectRequest{Episode: id})
			if e != nil {
				return e
			}
			var mf downloadManifest
			if e = json.Unmarshal(inspect.GetManifestJson(), &mf); e != nil {
				return e
			}
			if e = os.MkdirAll(stage, 0o750); e != nil {
				return e
			}
			for _, meta := range mf.Files {
				if e = downloadOne(cmd.Context(), client, id, stage, meta.Path, meta.Size, meta.SHA256); e != nil {
					return e
				}
			}
			if e = os.WriteFile(filepath.Join(stage, "manifest.json"), inspect.GetManifestJson(), 0o640); e != nil {
				return e
			}
			if e = os.Rename(stage, output); e != nil {
				return e
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Downloaded %s to %s\n", id, output)
			return nil
		})
	}}
	c.Flags().StringVarP(&output, "output", "o", "", "Destination directory (default episode ID)")
	return c
}

func downloadOne(ctx context.Context, c agentpbv2.DataServiceClient, id, root, rel string, size int64, wantHash string) error {
	if rel == "" || filepath.IsAbs(rel) || filepath.Clean(rel) != rel || strings.HasPrefix(rel, "..") {
		return errors.New("server returned unsafe file path")
	}
	p := filepath.Join(root, rel)
	if !strings.HasPrefix(p, root+string(os.PathSeparator)) {
		return errors.New("server file path escapes destination")
	}
	if e := os.MkdirAll(filepath.Dir(p), 0o750); e != nil {
		return e
	}
	f, e := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o640)
	if e != nil {
		return e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return e
	}
	offset := st.Size()
	if offset > size {
		return fmt.Errorf("staged file %s is larger than manifest", rel)
	}
	if _, e = f.Seek(offset, io.SeekStart); e != nil {
		return e
	}
	stream, e := c.Download(ctx, &agentpbv2.DataDownloadRequest{Episode: id, Path: rel, Offset: offset})
	if e != nil {
		return e
	}
	for {
		chunk, e := stream.Recv()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		if chunk.GetOffset() != offset {
			return fmt.Errorf("non-contiguous chunk for %s", rel)
		}
		if len(chunk.GetData()) > 0 {
			n, e := f.Write(chunk.GetData())
			if e != nil {
				return e
			}
			offset += int64(n)
		}
		if chunk.GetEof() {
			break
		}
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if offset != size {
		return fmt.Errorf("downloaded size mismatch for %s", rel)
	}
	if _, e = f.Seek(0, io.SeekStart); e != nil {
		return e
	}
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return e
	}
	if hex.EncodeToString(h.Sum(nil)) != wantHash {
		return fmt.Errorf("checksum mismatch for %s", rel)
	}
	return nil
}

func printEpisode(cmd *cobra.Command, e *agentpbv2.DataEpisode) error {
	if jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(e)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Episode %s: %s\n", e.GetId(), e.GetState())
	return nil
}
