//! Async dispatcher task — drains `WatchSignalBatch`es from the evaluator
//! channel and fans them out to each entry's configured targets.
//!
//! Targets per batch (not mutually exclusive):
//! - **Webhook** — `POST` JSON body to `webhook_url`.
//! - **NATS** — publish JSON body to `nats_subject`.
//!
//! Runs as a `tokio::spawn`ed task for the lifetime of the process.

use tokio::sync::mpsc;
use tracing::{debug, info, warn};

use super::types::WatchSignalBatch;

pub async fn watch_dispatcher(
    nats: async_nats::Client,
    mut rx: mpsc::UnboundedReceiver<WatchSignalBatch>,
) {
    let http = reqwest::Client::builder()
        .timeout(std::time::Duration::from_secs(5))
        .build()
        .expect("reqwest client build");

    while let Some(batch) = rx.recv().await {
        let payload = match serde_json::to_vec(&batch) {
            Ok(v)  => v,
            Err(e) => {
                warn!(watch_id=%batch.watch_id, err=%e, "watch_dispatcher: serialize failed");
                continue;
            }
        };

        if let Some(ref url) = batch.webhook_url {
            match http.post(url)
                .header("Content-Type", "application/json")
                .body(payload.clone())
                .send()
                .await
            {
                Ok(r) if r.status().is_success() => {
                    debug!(watch_id=%batch.watch_id, url=%url, "watch_dispatcher: webhook ok");
                }
                Ok(r) => {
                    warn!(watch_id=%batch.watch_id, url=%url,
                          status=%r.status(), "watch_dispatcher: webhook non-2xx");
                }
                Err(e) => {
                    warn!(watch_id=%batch.watch_id, url=%url, err=%e,
                          "watch_dispatcher: webhook error");
                }
            }
        }

        if let Some(ref subject) = batch.nats_subject {
            if let Err(e) = nats.publish(subject.clone(), payload.into()).await {
                warn!(watch_id=%batch.watch_id, subject=%subject, err=%e,
                      "watch_dispatcher: NATS publish failed");
            }
        }
    }

    info!("watch_dispatcher: channel closed, exiting");
}
