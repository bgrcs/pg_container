package main

import (
	"archive/tar"
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	cli "github.com/urfave/cli/v3"
)

//go:embed pg_dump
var pgDump []byte

//go:embed Dockerfile
var dockerfile []byte

func main() {
	cli := &cli.Command{
		Name:  "pg_container",
		Usage: "Make a re-usable Docker container from a live Postgres database",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:    "container",
				Aliases: []string{"c"},
				Usage:   "Automatically create a container from the generated image",
			},
		},
		UsageText: `pg_container [connection_url]

Example:
	pg_container postgres://user:password@localhost:5432/db`,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			connectionURL := cmd.Args().Get(0)

			if len(connectionURL) > 0 {
				containerFlag := cmd.Bool("container")

				processBackup(connectionURL, containerFlag)
			} else {
				cli.ShowAppHelp(cmd)
			}

			return nil
		},
	}

	if err := cli.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}
}

func processBackup(connectionURL string, createContainerFlag bool) {
	println("> Step 1: ⚙️ Processing dump")

	tmpDir := os.TempDir()
	pgDumpPath := filepath.Join(tmpDir, "pg_dump")

	if _, err := os.Stat(pgDumpPath); os.IsNotExist(err) {
		tmpFile, err := os.Create(pgDumpPath)
		if err != nil {
			panic(err)
		}
		defer tmpFile.Close()

		_, err = tmpFile.Write(pgDump)
		if err != nil {
			panic(err)
		}

		err = tmpFile.Chmod(0755)
		if err != nil {
			panic(err)
		}
	} else if err != nil {
		panic(err)
	}

	databaseName, err := extractDatabaseName(connectionURL)

	if err != nil {
		panic(err)
	}

	tarBuffer := new(bytes.Buffer)

	tw := tar.NewWriter(tarBuffer)

	runPgDumpToTar(pgDumpPath, connectionURL, tw)

	apiClient, err := client.NewClientWithOpts(client.FromEnv)

	if err != nil {
		panic(err)
	}
	defer apiClient.Close()

	imageName := createDockerImage(databaseName, apiClient, tw, tarBuffer, databaseName)

	if createContainerFlag {
		createContainer(apiClient, databaseName, imageName)
	}
}

func extractDatabaseName(connectionURL string) (string, error) {
	u, err := url.Parse(connectionURL)
	if err != nil {
		return "", fmt.Errorf("Invalid Postgres connection URL: %w", err)
	}

	dbName := strings.TrimPrefix(u.Path, "/")

	if dbName == "" {
		if u.User != nil {
			dbName = u.User.Username()
		}
	}

	if dbName == "" {
		return "", fmt.Errorf("No database name or username found in the connection URL")
	}

	return dbName, nil
}

func createDockerImage(imageName string, apiClient *client.Client, tw *tar.Writer, buffer *bytes.Buffer, databaseName string) string {
	println("> Step 2: 🖼️  Creating Docker image")

	err := tw.WriteHeader(&tar.Header{
		Name: "Dockerfile",
		Size: int64(len(dockerfile)),
		Mode: 0600,
	})
	if err != nil {
		log.Fatalf("Failed to write tar header: %s", err)
	}
	_, err = tw.Write(dockerfile)
	if err != nil {
		log.Fatalf("Failed to write Dockerfile to tar: %s", err)
	}
	tw.Close()

	buildContext := bytes.NewReader(buffer.Bytes())

	currentTime := time.Now()

	formattedTime := currentTime.Format("2006-01-02-1504")

	fullImageName := imageName + "-" + formattedTime + ":latest"

	buildOptions := types.ImageBuildOptions{
		Tags:        []string{fullImageName},
		Dockerfile:  "Dockerfile",
		Remove:      true,
		ForceRemove: true,
		BuildArgs: map[string]*string{
			"DB_NAME": &databaseName,
		},
	}

	ctx := context.Background()
	buildResponse, err := apiClient.ImageBuild(ctx, buildContext, buildOptions)

	if err != nil {
		panic(err)
	}

	if buildResponse.Body == nil {
		panic("Unknown error occurred when building docker image")
	}

	defer func() {
		if buildResponse.Body != nil {
			buildResponse.Body.Close()
		}
	}()

	io.Copy(io.Discard, buildResponse.Body)

	fmt.Printf("✅ Image built successfully with name: %s\n", fullImageName)

	return fullImageName
}

func createContainer(apiClient *client.Client, databaseName string, imageName string) {
	println("> Step 2: 📦 Creating a container")

	containerConfig := &container.Config{
		Image: imageName,
		Env:   []string{},

		ExposedPorts: nat.PortSet{
			"5432/tcp": struct{}{},
		},
	}
	hostConfig := &container.HostConfig{
		PortBindings: nat.PortMap{
			"5432/tcp": []nat.PortBinding{
				{
					HostIP:   "127.0.0.1",
					HostPort: "5432",
				},
			},
		},
	}

	containerName := "postgres-" + databaseName + "-" + strconv.FormatInt(time.Now().Unix(), 10)

	_, err := apiClient.ContainerCreate(context.Background(), containerConfig, hostConfig, nil, nil, containerName)
	if err != nil {
		panic(err)
	}

	fmt.Printf("✅ Container created with name: %s\n", containerName)
}

func runPgDumpToTar(pgDumpPath, connectionURL string, tw *tar.Writer) error {
	var dumpBuffer bytes.Buffer
	var stderr bytes.Buffer

	cmd := exec.Command(pgDumpPath, connectionURL)
	cmd.Stderr = &stderr
	cmd.Stdout = &dumpBuffer

	if err := cmd.Start(); err != nil {
		panic(err)
	}

	if err := cmd.Wait(); err != nil {
		fmt.Println(stderr.String())
		panic(err)
	}

	dumpSize := int64(dumpBuffer.Len())

	tarHeader := &tar.Header{
		Name:     "dump.sql",
		Mode:     0777,
		Size:     dumpSize,
		Typeflag: tar.TypeReg,
	}

	if err := tw.WriteHeader(tarHeader); err != nil {
		panic(err)
	}

	if _, err := tw.Write(dumpBuffer.Bytes()); err != nil {
		panic(err)
	}

	return nil
}
