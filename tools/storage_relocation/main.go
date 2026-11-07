// Command storage_relocation coordinates an explicit offline storage move.
// Ordinary HTTP publication and service startup never invoke this command.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
	data "github.com/wuhy80/AgentForge-Ontology/services/data"
	metadata "github.com/wuhy80/AgentForge-Ontology/services/metadata"
)

type manifest struct {
	Format int                   `json:"format"`
	Items  []c.StorageRelocation `json:"items"`
}

type report struct {
	ManifestDigest string                      `json:"manifestDigest"`
	Phase          string                      `json:"phase"`
	CheckedAt      time.Time                   `json:"checkedAt"`
	Results        []c.StorageRelocationResult `json:"results,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func readManifest(path string) (manifest, string, error) {
	var value manifest
	f, err := os.Open(path)
	if err != nil {
		return value, "", errors.New("无法读取迁移清单")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 4*1024*1024+1))
	if err != nil || len(raw) > 4*1024*1024 {
		return value, "", errors.New("迁移清单不可读或超过4MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, "", errors.New("迁移清单结构不合法")
	}
	if decoder.Decode(new(any)) != io.EOF || value.Format != 1 || len(value.Items) == 0 || len(value.Items) > 16 {
		return value, "", errors.New("只接受格式1、1至16个模型的单份迁移清单")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return value, "", err
	}
	hash := sha256.Sum256(canonical)
	return value, hex.EncodeToString(hash[:]), nil
}

func writeReport(path string, value report) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".relocation-*.json")
	if err != nil {
		return errors.New("无法创建迁移报告临时文件")
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("迁移报告写入失败")
	}
	if err = os.Rename(name, path); err != nil {
		return errors.New("迁移报告替换失败")
	}
	return nil
}

func ensureOffline(addresses []string) error {
	for _, address := range addresses {
		host, _, err := net.SplitHostPort(address)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			return errors.New("维护探针必须指定本机回环IP和端口")
		}
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			conn.Close()
			return fmt.Errorf("请先停止本地服务：%s 仍在监听", address)
		}
	}
	return nil
}

func acquireLock(ctx context.Context, engine *storage.Engine, name string) (func(), error) {
	conn, err := engine.DB().Conn(ctx)
	if err != nil {
		return nil, errors.New("无法取得维护连接")
	}
	var acquired sql.NullInt64
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK(?,0)", name).Scan(&acquired); err != nil || !acquired.Valid || acquired.Int64 != 1 {
		conn.Close()
		return nil, errors.New("其他进程已持有本批维护锁")
	}
	return func() {
		release, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(release, "DO RELEASE_LOCK(?)", name)
		_ = conn.Close()
	}, nil
}

func run() error {
	manifestPath := flag.String("manifest", "", "固定迁移清单JSON；不含连接凭据")
	sealPath := flag.String("seal", "", "将候选定义按实际Go协议计算摘要并保存为新的固定清单，不访问数据库")
	reportPath := flag.String("report", "", "原子保存迁移阶段与核验摘要的JSON路径")
	apply := flag.Bool("apply", false, "显式执行；默认仅检查并生成计划")
	offline := flag.Bool("offline", false, "确认本地Metadata和Data已停止，失败后保持停机并以同清单恢复")
	metadataAddress := flag.String("metadata-address", "127.0.0.1:18080", "本地Metadata监听地址")
	dataAddress := flag.String("data-address", "127.0.0.1:18081", "本地Data监听地址")
	flag.Parse()
	if *manifestPath == "" {
		return errors.New("必须指定 -manifest")
	}
	if *apply && !*offline {
		return errors.New("执行迁移必须显式传入 -offline 并先停止两个服务")
	}
	value, manifestDigest, err := readManifest(*manifestPath)
	if err != nil {
		return err
	}
	if *sealPath != "" {
		if *apply {
			return errors.New("清单定稿不能与 -apply 同时使用")
		}
		for i := range value.Items {
			raw, err := json.Marshal(value.Items[i].Definition)
			if err != nil {
				return err
			}
			hash := sha256.Sum256(raw)
			sealed := hex.EncodeToString(hash[:])
			if value.Items[i].Digest != "" && value.Items[i].Digest != sealed {
				return errors.New("候选摘要不匹配，请核对原清单")
			}
			value.Items[i].Digest = sealed
		}
		raw, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		f, err := os.OpenFile(*sealPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return errors.New("无法创建新清单，已有固定清单不会覆盖")
		}
		if _, err = f.Write(raw); err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return errors.New("固定清单未完整写入，请保留并检查文件")
		}
		if closeErr != nil {
			return closeErr
		}
		fmt.Fprintln(os.Stdout, "迁移清单已定稿；未连接数据库")
		return nil
	}
	if *reportPath == "" {
		return errors.New("计划或执行迁移必须指定 -report")
	}
	if existing, readErr := os.ReadFile(*reportPath); readErr == nil {
		var previous report
		if json.Unmarshal(existing, &previous) != nil || previous.ManifestDigest != manifestDigest {
			return errors.New("报告属于另一份迁移清单，请保留原报告并使用新的报告路径")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return errors.New("原迁移报告无法读取")
	}
	if *apply {
		if err := ensureOffline([]string{*metadataAddress, *dataAddress}); err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	metaEngine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: os.Getenv("ONTOLOGY_RELOCATION_METADATA_DSN"), MaxOpen: 5, MaxIdle: 2})
	if err != nil {
		return errors.New("Metadata控制库连接失败，请检查专用环境变量")
	}
	defer metaEngine.Close()
	dataEngine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: os.Getenv("ONTOLOGY_RELOCATION_DATA_DSN"), MaxOpen: 5, MaxIdle: 2})
	if err != nil {
		return errors.New("Data控制库连接失败，请检查专用环境变量")
	}
	defer dataEngine.Close()
	lockID := sha256.Sum256([]byte(value.Items[0].Definition.TenantID))
	lockName := "af-storage-relocation:" + hex.EncodeToString(lockID[:16])
	unlockMeta, err := acquireLock(ctx, metaEngine, lockName+":metadata")
	if err != nil {
		return err
	}
	defer unlockMeta()
	unlockData, err := acquireLock(ctx, dataEngine, lockName+":data")
	if err != nil {
		return err
	}
	defer unlockData()
	if err = metadata.ValidateDefaultStorageRelocation(ctx, metaEngine, value.Items); err != nil {
		return err
	}
	config := data.Config{Engine: dataEngine, DataSourceKey: os.Getenv("ONTOLOGY_DATA_SOURCE_KEY")}
	results, err := data.PlanDefaultStorageRelocation(ctx, config, value.Items)
	if err != nil {
		return err
	}
	state := report{ManifestDigest: manifestDigest, Phase: "planned", CheckedAt: time.Now().UTC(), Results: results}
	if err = writeReport(*reportPath, state); err != nil {
		return err
	}
	if !*apply {
		return json.NewEncoder(os.Stdout).Encode(state)
	}
	// These service-owned functions validate their own rows. The command never
	// writes private control tables or accepts arbitrary SQL from the manifest.
	results, err = data.PrepareDefaultStorageRelocation(ctx, config, value.Items)
	if err != nil {
		return fmt.Errorf("目标准备未完成，保留停机并使用同一清单恢复：%w", err)
	}
	state.Results, state.Phase, state.CheckedAt = results, "data_prepared", time.Now().UTC()
	if err = writeReport(*reportPath, state); err != nil {
		return err
	}
	if err = metadata.CommitDefaultStorageRelocation(ctx, metaEngine, value.Items); err != nil {
		return fmt.Errorf("元数据提交未确认，保留停机并使用同一清单恢复：%w", err)
	}
	state.Phase, state.CheckedAt = "metadata_committed", time.Now().UTC()
	if err = writeReport(*reportPath, state); err != nil {
		return err
	}
	if err = metadata.VerifyDefaultStorageRelocation(ctx, metaEngine, value.Items); err != nil {
		return err
	}
	if err = data.ActivateDefaultStorageRelocation(ctx, config, value.Items); err != nil {
		return fmt.Errorf("激活未完成，保留停机并使用同一清单恢复：%w", err)
	}
	state.Phase, state.CheckedAt = "complete", time.Now().UTC()
	if err = writeReport(*reportPath, state); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(state)
}
