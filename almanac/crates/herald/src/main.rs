mod handler;
mod registry;

use anyhow::Result;
use async_nats::jetstream::consumer::{pull, AckPolicy};
use handler::Handler;
use tracing::info;

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| "herald=info".into()),
        )
        .init();

    let nats_url = std::env::var("NATS_URL").unwrap_or_else(|_| "nats://localhost:4222".into());

    info!(url = %nats_url, "connecting to NATS");
    let client = async_nats::connect(&nats_url).await?;
    info!("connected to NATS");

    // JetStream: get BARS stream and create/attach durable pull consumer "herald".
    // This gives us replay-on-restart, exactly-once delivery (with ack), and
    // backfill of any bars published while herald was down.
    let js = async_nats::jetstream::new(client.clone());
    let stream = js.get_stream("BARS").await?;
    let consumer = stream
        .get_or_create_consumer(
            "herald",
            pull::Config {
                durable_name: Some("herald".into()),
                filter_subject: "bars.>".into(),
                ack_policy: AckPolicy::Explicit,
                ..Default::default()
            },
        )
        .await?;
    info!("JetStream pull consumer 'herald' ready on BARS stream");

    let handler = Handler::new(client, consumer);
    handler.run().await?;

    Ok(())
}
