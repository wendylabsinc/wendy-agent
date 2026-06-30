package optimize

import "testing"

func TestParseDockerfile(t *testing.T) {
	src := "# comment\n" +
		"FROM --platform=linux/amd64 python:3.12-slim\n" +
		"ARG WENDY_DEBUG=0\n" +
		"RUN --mount=type=cache,target=/root/.cargo \\\n" +
		"    cargo build --release\n" +
		"COPY . .\n"

	df := ParseDockerfile("Dockerfile", []byte(src))

	if len(df.Instructions) != 4 {
		t.Fatalf("got %d instructions, want 4: %+v", len(df.Instructions), df.Instructions)
	}

	from := df.Instructions[0]
	if from.Cmd != "FROM" || from.Line != 2 {
		t.Fatalf("FROM = %+v, want Cmd=FROM Line=2", from)
	}
	if len(from.Flags) != 1 || from.Flags[0] != "--platform=linux/amd64" {
		t.Fatalf("FROM flags = %v, want [--platform=linux/amd64]", from.Flags)
	}
	if from.Args != "python:3.12-slim" {
		t.Fatalf("FROM args = %q, want python:3.12-slim", from.Args)
	}

	run := df.Instructions[2]
	if run.Cmd != "RUN" || run.Line != 4 {
		t.Fatalf("RUN = %+v, want Cmd=RUN Line=4", run)
	}
	if len(run.Flags) != 1 || run.Flags[0] != "--mount=type=cache,target=/root/.cargo" {
		t.Fatalf("RUN flags = %v", run.Flags)
	}
	if run.Args != "cargo build --release" {
		t.Fatalf("RUN args = %q, want 'cargo build --release'", run.Args)
	}
}
