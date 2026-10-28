# Lab cluster

![The lab cluster](../docs/assets/lab.png)

A throwaway Kubernetes cluster that runs a real LLM, so Sophron has something to watch and break.

| What | Details |
|---|---|
| Cluster | kind: 1 control plane + 2 workers, as Docker containers |
| Model | Qwen2.5 1.5B Instruct (4-bit), served on CPU by llama.cpp |
| Monitoring | Prometheus + Grafana (kube-prometheus-stack) |
| Load | A Job with 4 workers sending chat requests |
| Machine | Ubuntu, 8 vCPU, 32 GB RAM, 40 GB+ disk (we use an EC2 t3.2xlarge) |

## 1. Prepare the EC2 instance

- Security group: allow port 22 from **your IP only**. Open nothing else.
- Start the instance and note its public IP. It changes after every stop/start.

```bash
ssh -i ~/.ssh/<your-key>.pem ubuntu@<public-ip>
```

## 2. Get the code

The repo is private, so use a GitHub token (fine-grained, read-only, `sophron` repo only):

```bash
git clone https://github.com/Shiva-2103/sophron.git
cd sophron
git switch phase-1/kind-lab
```

## 3. Install tools (once)

```bash
make lab-tools        # Docker, kind, kubectl, helm (asks for sudo)
newgrp docker         # use Docker without sudo in this shell
```

## 4. Start the lab

```bash
make lab-up           # about 5 to 10 minutes the first time
make lab-check        # PASS/FAIL for each Phase 1 goal
```

## 5. Send traffic and watch the metrics

```bash
make lab-load         # 4 workers x 25 requests
make lab-check        # tokens per second should now be above 0
```

## 6. Open Grafana and Prometheus from your laptop

On the EC2 machine:

```bash
make lab-ports
make lab-grafana-password
```

On your laptop, open a tunnel in a new terminal and keep it open:

```bash
ssh -i ~/.ssh/<your-key>.pem -N \
  -L 3000:localhost:3000 -L 9090:localhost:9090 -L 8080:localhost:8080 \
  ubuntu@<public-ip>
```

Then browse:

| UI | Link |
|---|---|
| Grafana (user `admin`) | http://localhost:3000 |
| Prometheus | http://localhost:9090 |

Try this query in Prometheus: `rate(llamacpp:tokens_predicted_total[1m])`

## Commands

| Command | Does |
|---|---|
| `make lab-up` | Create everything |
| `make lab-check` | Check the Phase 1 goals |
| `make lab-load` | Send requests (`REQUESTS=100` for more) |
| `make lab-status` | Show nodes and pods |
| `make lab-ports` / `lab-ports-stop` | Open or close local UI ports |
| `make lab-down` | Delete the cluster |

## Save money

Stop the EC2 instance when you're done. You pay per hour while it runs. The cluster comes back with `make lab-up` after a restart (kind containers don't survive a reboot cleanly, so `make lab-down && make lab-up` if anything looks stuck).

## If something fails

| Symptom | Try |
|---|---|
| `permission denied ... docker.sock` | `newgrp docker`, or log out and back in |
| Model pod stuck in `Init` | `kubectl -n llm logs deploy/llm-server -c download-model` (model download) |
| Model pod `CrashLoopBackOff` | `kubectl -n llm logs deploy/llm-server` |
| `up=0` in lab-check | Wait 30 seconds, Prometheus scrapes every 15 seconds |
| Pods `Pending` | `kubectl describe pod ...` and look at Events at the bottom |
