package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"time"

	hfd "github.com/bodaay/HuggingFaceModelDownloader/hfdownloader"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
)

const VERSION = "1.4.2"

type Config struct {
	NumConnections     int    `json:"num_connections"`
	RequiresAuth       bool   `json:"requires_auth"`
	AuthToken          string `json:"auth_token"`
	ModelName          string `json:"model_name"`
	DatasetName        string `json:"dataset_name"`
	Branch             string `json:"branch"`
	Storage            string `json:"storage"`
	OneFolderPerFilter bool   `json:"one_folder_per_filter"`
	SkipSHA            bool   `json:"skip_sha"`
	// Install            bool   `json:"install"`
	// InstallPath        string `json:"install_path"`
	MaxRetries    int    `json:"max_retries"`
	RetryInterval int    `json:"retry_interval"`
	JustDownload  bool   `json:"just_download"`
	SilentMode    bool   `json:"silent_mode"`
	UseR2         bool   `json:"use_r2"`
	R2BucketName  string `json:"r2_bucket_name"`
	R2AccountID   string `json:"r2_account_id"`
	R2AccessKey   string `json:"r2_access_key"`
	R2SecretKey   string `json:"r2_secret_key"`
	SkipLocal     bool   `json:"skip_local"`
	R2Subfolder   string `json:"r2_subfolder"`
	HFPrefix      string `json:"hf_prefix"`
	MaxWorkers    int    `json:"max_workers"`   // Maximum number of worker goroutines
}

// DefaultConfig returns a config instance populated with default values.
func DefaultConfig() Config {
	return Config{
		NumConnections: 5,
		Branch:         "main",
		Storage:        "./",
		MaxRetries:     3,
		RetryInterval:  5,
		R2Subfolder:    "hf_dataset",
		MaxWorkers:     16, // Default to 16 worker goroutines
	}
}

func LoadConfig() (*Config, error) {
	config := DefaultConfig() // Use defaults as a base
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	configPath := filepath.Join(homeDir, ".config", "hfdownloader.json")

	file, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return &config, nil // Return defaults if file does not exist
	} else if err == nil {
		if err := json.Unmarshal(file, &config); err != nil {
			return nil, err
		}
	}

	// Check if an environment variable to always enable the 'just download' feature is enabled
	envVar := os.Getenv("HFDOWNLOADER_JUST_DOWNLOAD")
	if envVar == "1" || envVar == "true" {
		config.Storage = "./" // Set storage to current directory
	}

	return &config, nil
}

func generateConfigFile() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	configPath := filepath.Join(homeDir, ".config", "hfdownloader.json")

	config := DefaultConfig()

	file, err := os.OpenFile(configPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(config); err != nil {
		return err
	}

	fmt.Printf("Generated config file at: %s\n", configPath)
	return nil
}

func main() {
	config, err := LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}
	var justDownload bool
	var (
		install          bool
		installPath      string
		cleanupCorrupted bool
	)
	ShortString := fmt.Sprintf("a Simple HuggingFace Models Downloader Utility\nVersion: %s", VERSION)
	currentPath, err := os.Executable()
	if err != nil {
		log.Printf("Failed to get execuable path, %s", err)
	}
	if currentPath != "" {
		ShortString = fmt.Sprintf("%s\nRunning on: %s", ShortString, currentPath)
	}
	rootCmd := &cobra.Command{
		Use:           "hfdownloader [model]",
		Short:         ShortString,
		SilenceErrors: true,
		SilenceUsage:  true,
		Args: func(cmd *cobra.Command, args []string) error {
			if justDownload && len(args) < 1 {
				return errors.New("requires a model name argument when using -j")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if justDownload {
				config.ModelName = args[0] // Use the first argument as the model name
				config.Storage = "./"
			}
			// Validate the ModelName parameter
			// if !hfdn.IsValidModelName(modelName) { Just realized there are indeed models that don't follow this format :)
			// 	// fmt.Println("Error:", err)
			// 	return fmt.Errorf("Invailid Model Name, it should follow the pattern: ModelAuthor/ModelName")
			// }
			// Dynamic configuration updates (e.g., for AuthToken)
			if config.AuthToken == "" {
				config.AuthToken = os.Getenv("HF_TOKEN")
				if config.AuthToken == "" {
					config.AuthToken = os.Getenv("HUGGING_FACE_HUB_TOKEN")
					if config.AuthToken != "" {
						fmt.Println("DeprecationWarning: The environment variable 'HUGGING_FACE_HUB_TOKEN' is deprecated and will be removed in a future version. Please use 'HF_TOKEN' instead.")
					}
				}
			}
			if install {
				if err := installBinary(installPath); err != nil {
					log.Fatal(err)
				}
				os.Exit(0)
			}
			var IsDataset bool
			ModelOrDataSet := config.ModelName
			if config.ModelName != "" {
				fmt.Println("Model:", config.ModelName)
				IsDataset = false
			} else if config.DatasetName != "" {
				fmt.Println("Dataset:", config.DatasetName)
				IsDataset = true
				ModelOrDataSet = config.DatasetName
			} else {
				cmd.Help()
				return fmt.Errorf("Error: You must set either modelName or datasetName.")
			}

			_ = godotenv.Load() // Load .env file if exists

			if config.AuthToken == "" {
				config.AuthToken = os.Getenv("HF_TOKEN")
				if config.AuthToken == "" {
					config.AuthToken = os.Getenv("HUGGING_FACE_HUB_TOKEN")
					if config.AuthToken != "" {
						fmt.Println("DeprecationWarning: The environment variable 'HUGGING_FACE_HUB_TOKEN' is deprecated and will be removed in a future version. Please use 'HF_TOKEN' instead.")
					}
				}
			}

			fmt.Printf("Branch: %s\nStorage: %s\nNumberOfConcurrentConnections: %d\nAppend Filter Names to Folder: %t\nSkip SHA256 Check: %t\nToken: %s\n",
				config.Branch, config.Storage, config.NumConnections, config.OneFolderPerFilter, config.SkipSHA, config.AuthToken)

			var r2cfg *hfd.R2Config
			if config.UseR2 {
				// Load credentials from env
				accountID := os.Getenv("R2_ACCOUNT_ID")
				accessKey := os.Getenv("R2_WRITE_ACCESS_KEY_ID")
				secretKey := os.Getenv("R2_WRITE_SECRET_ACCESS_KEY")
				if accountID == "" || accessKey == "" || secretKey == "" {
					log.Fatal("R2 credentials not found in environment variables")
				}

				// Use config.R2BucketName if provided; otherwise, try the env variable R2_BUCKET_NAME;
				// if still empty, fallback to accountID.
				bucketName := config.R2BucketName
				if bucketName == "" {
					bucketName = os.Getenv("R2_BUCKET_NAME")
					if bucketName == "" {
						bucketName = accountID
					}
				}

				// Use the provided subfolder or default if empty
				subfolder := config.R2Subfolder
				if subfolder == "" {
					subfolder = "hf_dataset"
				}

				r2cfg = &hfd.R2Config{
					AccountID:       accountID,
					AccessKeyID:     accessKey,
					AccessKeySecret: secretKey,
					BucketName:      bucketName,
					Region:          "auto",
					Subfolder:       subfolder,
				}
			}

			if cleanupCorrupted {
				ctx := context.Background()
				prefix := r2cfg.Subfolder + "/" // ensure trailing slash so keys match
				if err := hfd.CleanupCorruptedFiles(ctx, r2cfg, prefix, config.NumConnections); err != nil {
					log.Fatalf("Failed to cleanup corrupted files: %v", err)
				}
				fmt.Println("Cleanup completed")
				return nil
			}

			for i := 0; i < config.MaxRetries; i++ {
				if err := hfd.DownloadModel(
					ModelOrDataSet,            // model name
					config.OneFolderPerFilter, // append filter to path
					config.SkipSHA,            // skip SHA check
					IsDataset,                 // is dataset
					config.Storage,            // local temp path
					config.Branch,             // branch
					config.NumConnections,     // concurrent connections
					config.AuthToken,          // HF token
					config.SilentMode,         // silent mode
					r2cfg,                     // R2 config
					config.SkipLocal,          // skipLocal - use SkipLocal flag
					config.HFPrefix,           // HF prefix
					config.MaxWorkers,         // max workers for parallel downloads
				); err != nil {
					fmt.Printf("Warning: attempt %d / %d failed, error: %s\n", i+1, config.MaxRetries, err)
					time.Sleep(time.Duration(config.RetryInterval) * time.Second)
					continue
				}
				fmt.Printf("\nDownload of %s completed successfully\n", ModelOrDataSet)
				return nil
			}
			return fmt.Errorf("failed to download %s after %d attempts", ModelOrDataSet, config.MaxRetries)
		},
	}

	// Setup flags and bind them to config properties
	rootCmd.PersistentFlags().StringVarP(&config.ModelName, "model", "m", config.ModelName, "Model name to download")
	rootCmd.PersistentFlags().StringVarP(&config.DatasetName, "dataset", "d", config.DatasetName, "Dataset name to download")
	rootCmd.PersistentFlags().StringVarP(&config.Branch, "branch", "b", config.Branch, "Branch of the model or dataset")
	rootCmd.PersistentFlags().StringVarP(&config.Storage, "storage", "s", config.Storage, "Storage path for downloads")
	rootCmd.PersistentFlags().IntVarP(&config.MaxWorkers, "concurrent", "c", config.MaxWorkers, "Number of concurrent download workers")
	rootCmd.PersistentFlags().StringVarP(&config.AuthToken, "token", "t", config.AuthToken, "HuggingFace Auth Token")
	rootCmd.PersistentFlags().BoolVarP(&config.OneFolderPerFilter, "appendFilterFolder", "f", config.OneFolderPerFilter, "Append filter name to folder")
	rootCmd.PersistentFlags().BoolVarP(&config.SkipSHA, "skipSHA", "k", config.SkipSHA, "Skip SHA256 hash check")
	rootCmd.PersistentFlags().IntVar(&config.MaxRetries, "maxRetries", config.MaxRetries, "Maximum number of retries for downloads")
	rootCmd.PersistentFlags().IntVar(&config.RetryInterval, "retryInterval", config.RetryInterval, "Interval between retries in seconds")
	rootCmd.PersistentFlags().BoolVarP(&justDownload, "justDownload", "j", config.JustDownload, "Just download the model to the current directory and assume the first argument is the model name")
	rootCmd.Flags().BoolVarP(&install, "install", "i", false, "Install the binary to the OS default bin folder, Unix-like operating systems only")

	rootCmd.Flags().StringVarP(&installPath, "installPath", "p", "/usr/local/bin/", "install Path (optional)")
	rootCmd.PersistentFlags().BoolVarP(&config.SilentMode, "silentMode", "q", config.SilentMode, "Disable progress bar output printing")

	// Add the generate-config command
	generateCmd := &cobra.Command{
		Use:   "generate-config",
		Short: "Generates an example configuration file with default values",
		RunE: func(cmd *cobra.Command, args []string) error {
			return generateConfigFile()
		},
	}

	rootCmd.AddCommand(generateCmd)

	// Add new flags
	rootCmd.PersistentFlags().BoolVar(&config.UseR2, "r2", false, "Upload to Cloudflare R2")
	rootCmd.PersistentFlags().StringVar(&config.R2BucketName, "r2-bucket", "", "R2 bucket name")
	rootCmd.PersistentFlags().StringVar(&config.R2AccountID, "r2-account", "", "R2 account ID")
	rootCmd.PersistentFlags().StringVar(&config.R2AccessKey, "r2-access-key", "", "R2 access key")
	rootCmd.PersistentFlags().StringVar(&config.R2SecretKey, "r2-secret-key", "", "R2 secret key")
	rootCmd.PersistentFlags().BoolVar(&config.SkipLocal, "skip-local", false, "Skip local storage when using R2")
	rootCmd.PersistentFlags().BoolVar(&cleanupCorrupted, "cleanup-corrupted", false, "Clean up corrupted parquet files")
	rootCmd.PersistentFlags().StringVar(&config.R2Subfolder, "r2-subfolder", config.R2Subfolder, "Subfolder on your R2 bucket (e.g. hf_dataset)")
	rootCmd.PersistentFlags().StringVar(&config.HFPrefix, "hf-prefix", "", "Optional prefix to only fetch files from a specific folder in the HF datasets repo")

	if err := rootCmd.Execute(); err != nil {
		log.Fatalln("Error:", err)
	}
}

func installBinary(installPath string) error {
	if runtime.GOOS == "windows" {
		return errors.New("the install command is not supported on Windows")
	}

	exePath, err := os.Executable()
	if err != nil {
		return err
	}

	dst := path.Join(installPath, filepath.Base(exePath))

	// Check if we need sudo for either removing the existing binary or copying the new one
	needsSudo := false

	// Check if the binary already exists
	if _, err := os.Stat(dst); err == nil {
		// Try to remove the existing binary
		err := os.Remove(dst)
		if err != nil {
			if os.IsPermission(err) {
				needsSudo = true
			} else {
				return err
			}
		}
	}

	// Open the source file
	srcFile, err := os.Open(exePath)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	// Try to copy the file
	err = copyFile(dst, srcFile)
	if err != nil {
		if os.IsPermission(err) {
			needsSudo = true
		} else {
			return err
		}
	}

	// If we need sudo, handle both removal and copy with elevated privileges
	if needsSudo {
		fmt.Printf("Require sudo privileges to complete installation at: %s\n", installPath)
		cmd := exec.Command("sudo", "sh", "-c", fmt.Sprintf("rm -f %s && cp %s %s", dst, exePath, dst))
		if err := cmd.Run(); err != nil {
			return err
		}
	}

	log.Printf("The binary has been successfully installed to %s", dst)
	return nil
}

// copyFile is a helper function to copy a file with specific permission
func copyFile(dst string, src *os.File) error {
	// Open destination file and ensure it gets closed
	dstFile, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	// Copy the file content
	if _, err := io.Copy(dstFile, src); err != nil {
		return err
	}
	return nil
}
