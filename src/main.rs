use kube::api::{Node, Patch, PatchParams};
use k8s_openapi::api::core::v1::{Node as K8sNode, Taint};
use kube::runtime::controller::Action;
use kube::Client;
use anyhow::{Context, Result};

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
            let taint = Taint {
                key: "multus".to_string(),
                value: "unavailable".to_string(),
                effect: "NoSchedule".to_string(),
                ..Default::default()
            };

            // Patch the node with taint
            let patch = Patch::Apply(&K8sNode {
                spec: Some(K8sNodeSpec {
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

