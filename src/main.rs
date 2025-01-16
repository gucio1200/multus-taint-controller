use kube::Client;
use kube::api::{Patch, PatchParams, ListParams};
use kube_runtime::controller::Action;
use k8s_openapi::api::core::v1::{Node as K8sNode, Pod};
use kube::Api;
use anyhow::{Result, Context};

async fn taint_node_if_needed(
    nodes: &Api<K8sNode>,
    pods: &Api<Pod>,
    label_selector: &str,
) -> Result<()> {
    // Check if the Multus pods are ready
    let ready_multus_pods = check_multus_readiness(pods, label_selector).await?;

    // If Multus is not ready, taint the nodes
    if ready_multus_pods.is_empty() {
        // Fetch all nodes
        let nodes = nodes.list(&ListParams::default()).await?;

        for node in nodes.items {
            let taint = k8s_openapi::api::core::v1::Taint {
                key: "multus".to_string(),
                value: Some("unavailable".to_string()),  // wrap value in Some
                effect: Some("NoSchedule".to_string()),  // wrap effect in Some
                ..Default::default()
            };

            // Patch the node with taint
            let patch = Patch::Apply(&K8sNode {
                spec: Some(k8s_openapi::api::core::v1::NodeSpec {
                    taints: Some(vec![taint]),
                    ..Default::default()
                }),
                ..node
            });

            nodes.patch(&node.metadata.name.unwrap(), &PatchParams::default(), &patch).await?;
            println!("Tainted node {} because Multus is not ready.", node.metadata.name.unwrap());
        }
    }

    Ok(())
}

async fn check_multus_readiness(pods: &Api<Pod>, label_selector: &str) -> Result<Vec<String>> {
    let pod_list = pods.list(&ListParams::default()).await?;

    let mut ready_pods = Vec::new();
    for pod in pod_list.items {
        if let Some(labels) = &pod.metadata.labels {
            if labels.get("multus") == Some(&label_selector) {
                if let Some(status) = &pod.status {
                    if status.phase == Some("Running".to_string()) {
                        ready_pods.push(pod.metadata.name.unwrap());
                    }
                }
            }
        }
    }

    Ok(ready_pods)
}

#[tokio::main]
async fn main() -> Result<()> {
    let client = Client::try_default().await?;
    let pods = Api::<Pod>::all(client.clone());
    let nodes = Api::<K8sNode>::all(client.clone());

    // Label selector to identify multus pods
    let label_selector = "some-label-selector";

    // Run tainting logic
    taint_node_if_needed(&nodes, &pods, label_selector).await?;

    Ok(())
}
