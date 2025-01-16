use kube::{Client, Api};
use k8s_openapi::api::core::v1::{Pod, Node};
use kube_leader_election::LeaderElectionConfig;
use anyhow::{Result};
use tokio::time::Duration;
use std::env;

#[derive(Debug)]
struct ControllerConfig {
    label_selector: String,
}

async fn check_multus_readiness(pods: &Api<Pod>, label_selector: &str) -> Result<Vec<String>> {
    let pod_list = pods.list(&Default::default()).await?;
    let ready_pods: Vec<String> = pod_list.items.into_iter()
        .filter(|pod| pod.metadata.labels.as_ref().map_or(false, |labels| {
            labels.get("multus") == Some(&label_selector)  // Dereference `label_selector` correctly
        }))
        .map(|pod| pod.metadata.name.unwrap_or_default())
        .collect();
    Ok(ready_pods)
}

async fn taint_node_if_needed(nodes: &Api<Node>, pods: &Api<Pod>, label_selector: &str) -> Result<()> {
    let ready_pods = check_multus_readiness(pods, label_selector).await?;
    let nodes_list = nodes.list(&Default::default()).await?;
    
    for node in nodes_list.items {
        let mut taint_found = false;
        for pod in ready_pods.iter() {
            // Logic to check node tainting based on Multus readiness
            if node.metadata.name == Some(pod.clone()) {
                println!("Tainting node {}", node.metadata.name.as_deref().unwrap_or("unknown"));
                taint_found = true;
                // Tainting logic would be implemented here
            }
        }
        if !taint_found {
            println!("No Multus-related pod found for node {}, skipping tainting.", node.metadata.name.as_deref().unwrap_or("unknown"));
        }
    }
    Ok(())
}

async fn run_leader_election(client: Client, config: ControllerConfig) -> Result<()> {
    let pods: Api<Pod> = Api::all(client.clone());
    let nodes: Api<Node> = Api::all(client.clone());

    let leader_config = LeaderElectionConfig::new("multus-taint-controller")
        .with_election_duration(Duration::from_secs(15))
        .with_renew_deadline(Duration::from_secs(10))
        .with_relinquish_duration(Duration::from_secs(5));

    // Adjusted the leader election call, removing the closure with additional argument
    leader_config.run(client, |leader| async move {
        if leader {
            println!("I am the leader. Checking node taints...");
            taint_node_if_needed(&nodes, &pods, &config.label_selector).await.unwrap();
        } else {
            println!("Not the leader, skipping node tainting.");
        }
    }).await?;

    Ok(())
}

#[tokio::main]
async fn main() -> Result<()> {
    // Read the label selector from an environment variable, or default to "multus"
    let label_selector = env::var("LABEL_SELECTOR").unwrap_or_else(|_| "multus".to_string());

    println!("Using label selector: {}", label_selector);

    let client = Client::try_default().await?;
    let config = ControllerConfig {
        label_selector,
    };

    run_leader_election(client, config).await?;

    Ok(())
}
