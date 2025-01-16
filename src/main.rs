use kube::{
    api::{Api, ListParams, Patch, PatchParams},
    Client,
    core::lease::Lease,
};
use kube::runtime::leaderelection::{LeaderElection, LeaderElectionConfig};
use std::sync::Arc;
use tokio::sync::Mutex;
use std::env;
use dotenv::dotenv;
use serde_json::json;
use tokio::time::Duration;
use tracing::{info, warn};

const TAINT_KEY: &str = "multus-ready";
const TAINT_VALUE: &str = "false";
const TAINT_EFFECT: &str = "NoSchedule";

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    // Initialize logging
    tracing_subscriber::fmt::init();

    // Load environment variables from `.env` file (if present)
    dotenv().ok();

    // Read label selector from environment variables
    let multus_label_selector = env::var("MULTUS_LABEL_SELECTOR").unwrap_or_else(|_| "app=multus".to_string());

    info!("Using label selector: {}", multus_label_selector);

    // Initialize Kubernetes client
    let client = Client::try_default().await?;

    // Multus pods API (no namespace selector, using default)
    let pods: Api<kube::api::Pod> = Api::all(client.clone()); // This fetches pods from all namespaces
    let nodes: Api<kube::api::Node> = Api::all(client.clone());

    // Initialize leader lock
    let leader_lock = Arc::new(Mutex::new(false)); // Leader election state

    // Initialize leader election
    let leader_election = LeaderElection::new(&client, "multus-controller-leader", leader_lock.clone(), LeaderElectionConfig {
        lease_duration: Some(Duration::from_secs(15)),  // Lease duration
        renew_deadline: Some(Duration::from_secs(10)),  // Lease renewal deadline
        retry_period: Some(Duration::from_secs(2)),  // Retry period for acquiring lease
        ..Default::default()
    });

    // Start leader election process
    leader_election.start().await?;

    loop {
        if *leader_lock.lock().await {
            // Only the leader performs actions
            info!("Controller is the leader, managing taints...");
            match check_multus_readiness(&pods, &multus_label_selector).await {
                Ok(ready_nodes) => {
                    update_node_taints(&nodes, ready_nodes).await?;
                }
                Err(err) => {
                    warn!("Error checking Multus readiness: {:?}", err);
                }
            }
        } else {
            info!("Controller is not the leader, monitoring...");
        }

        // Wait for the next cycle
        tokio::time::sleep(Duration::from_secs(30)).await;
    }
}

/// Check the readiness of Multus pods
async fn check_multus_readiness(pods: &Api<kube::api::Pod>, label_selector: &str) -> anyhow::Result<Vec<String>> {
    let mut ready_nodes = vec![];
    let lp = ListParams::default().labels(label_selector);

    for pod in pods.list(&lp).await? {
        if let Some(status) = pod.status {
            if status.phase == Some("Running".to_string()) {
                if let Some(conditions) = status.conditions {
                    if conditions.iter().any(|c| c.type_ == "Ready" && c.status == "True") {
                        if let Some(node_name) = pod.spec.and_then(|spec| spec.node_name) {
                            ready_nodes.push(node_name);
                        }
                    }
                }
            }
        }
    }

    Ok(ready_nodes)
}

/// Update taints on nodes based on Multus readiness
async fn update_node_taints(
    nodes: &Api<kube::api::Node>,
    ready_nodes: Vec<String>,
) -> anyhow::Result<()> {
    let all_nodes = nodes.list(&ListParams::default()).await?;
    for node in all_nodes {
        let node_name = node.name_any();
        let taint = json!({
            "key": TAINT_KEY,
            "value": TAINT_VALUE,
            "effect": TAINT_EFFECT
        });

        if ready_nodes.contains(&node_name) {
            // Remove taint if Multus is ready
            if let Some(spec) = node.spec {
                if let Some(taints) = spec.taints {
                    if taints.iter().any(|t| t.key == TAINT_KEY) {
                        info!("Removing taint from node: {}", node_name);
                        let patch = json!({
                            "spec": {
                                "taints": taints.into_iter().filter(|t| t.key != TAINT_KEY).collect::<Vec<_>>()
                            }
                        });
                        nodes
                            .patch(&node_name, &PatchParams::default(), &Patch::Merge(&patch))
                            .await?;
                    }
                }
            }
        } else {
            // Add taint if Multus is not ready
            info!("Adding taint to node: {}", node_name);
            let patch = json!({
                "spec": {
                    "taints": [
                        {
                            "key": TAINT_KEY,
                            "value": TAINT_VALUE,
                            "effect": TAINT_EFFECT
                        }
                    ]
                }
            });
            nodes
                .patch(&node_name, &PatchParams::default(), &Patch::Merge(&patch))
                .await?;
        }
    }

    Ok(())
}
