package data

import (
	"context"
	"log"
	"net/http"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// RunLifecycleNoticeWorker periodically retries pending notification deliveries.
func RunLifecycleNoticeWorker(ctx context.Context, cfg Config) {
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	h := &handler{cfg: cfg, client: client}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := h.retryLifecycleBatch(ctx); err != nil && ctx.Err() == nil {
				log.Printf("lifecycle notification retry batch failed: %T", err)
			}
		}
	}
}

const lifecycleAutoRetryEligible = "notification_failed=TRUE AND notification_retry_at<=UTC_TIMESTAMP(6) AND created_at>=UTC_TIMESTAMP(6)-INTERVAL 24 HOUR"

func (h *handler) retryLifecycleBatch(ctx context.Context) error {
	db := h.cfg.Engine.DB()
	rows, err := db.QueryContext(ctx, `SELECT tenant_id,schema_id,entity_id,id,actor
 FROM af_data_record_lifecycle_runs WHERE `+lifecycleAutoRetryEligible+`
 ORDER BY notification_retry_at,tenant_id,schema_id,entity_id,id LIMIT 16`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type pending struct{ tenant, schema, record, run, actor string }
	var batch []pending
	for rows.Next() {
		var item pending
		if err := rows.Scan(&item.tenant, &item.schema, &item.record, &item.run, &item.actor); err != nil {
			return err
		}
		batch = append(batch, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range batch {
		result, err := db.ExecContext(ctx, `UPDATE af_data_record_lifecycle_runs
 SET notification_retry_at=UTC_TIMESTAMP(6)+INTERVAL 5 MINUTE WHERE `+lifecycleStageScope+`
 AND id=? AND `+lifecycleAutoRetryEligible, item.tenant, item.schema, item.record, item.run)
		if err != nil {
			return err
		}
		claimed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if claimed != 1 {
			continue
		}
		attempt, cancel := context.WithTimeout(ctx, 10*time.Second)
		actor := c.Principal{TenantID: item.tenant, Subject: item.actor, ExpiresAt: time.Now().Add(time.Minute).Unix()}
		// A failed attempt remains eligible after the claim expires.
		_ = h.retryLifecycleDelivery(attempt, actor, item.schema, item.record, item.run)
		cancel()
	}
	return nil
}
