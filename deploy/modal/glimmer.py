"""Serve Muse Glimmer 30B (open weights) on Modal as an inference
partner, as a chat-completions-compatible endpoint the Models panel can use.

    modal run deploy/modal/glimmer.py::download     # once: weights into a Volume (CPU)
    modal deploy deploy/modal/glimmer.py            # the endpoint (L4 GPU)

The endpoint needs a key (Modal secret "glimmer-key", GLIMMER_API_KEY) and
scales to zero after five idle minutes, so it only costs while it answers.
"""
import subprocess

import modal

REPO = "meta-models/Muse-Glimmer-30B-GGUF"
MODEL = "Muse-Glimmer-30B-KQuant-17GB-Q4_K_M.gguf"  # 15.6 GiB: fits a 24 GB L4 fully
MMPROJ = "mmproj-Muse-Glimmer-30B-Q4_K_M.gguf"  # vision projector: image input
WEIGHTS = "/weights"

app = modal.App("muse-glimmer")
weights = modal.Volume.from_name("muse-glimmer-weights", create_if_missing=True)

fetch_image = modal.Image.debian_slim().pip_install("huggingface_hub[hf_transfer]").env({"HF_HUB_ENABLE_HF_TRANSFER": "1"})
serve_image = modal.Image.from_registry("ghcr.io/ggml-org/llama.cpp:server-cuda", add_python="3.12").entrypoint([])


@app.function(image=fetch_image, volumes={WEIGHTS: weights}, timeout=3600)
def download():
    from huggingface_hub import hf_hub_download

    for f in (MODEL, MMPROJ):
        hf_hub_download(REPO, f, local_dir=WEIGHTS)
    weights.commit()
    print("weights ready")


@app.function(
    image=serve_image,
    gpu=["L4", "A10G"],  # 24 GB either way; take whichever is free
    volumes={WEIGHTS: weights},
    secrets=[modal.Secret.from_name("glimmer-key")],
    scaledown_window=300,  # stay up between requests of one session, then stop paying
    timeout=3600,
)
@modal.concurrent(max_inputs=4)
@modal.web_server(port=8080, startup_timeout=600)
def serve():
    import os

    # copy the weights to the container's own disk in one sequential pass:
    # llama-server's page-by-page reads straight from the network volume
    # took 18 minutes to load
    local = "/tmp/weights"
    os.makedirs(local, exist_ok=True)
    for f in (MODEL, MMPROJ):
        subprocess.run(["cp", f"{WEIGHTS}/{f}", f"{local}/{f}"], check=True)
    subprocess.Popen([
        "/app/llama-server",
        "-m", f"{local}/{MODEL}",
        "--mmproj", f"{local}/{MMPROJ}",
        "--host", "0.0.0.0", "--port", "8080",
        "--alias", "muse-glimmer-30b",
        "--api-key", os.environ["GLIMMER_API_KEY"],
        "-ngl", "99", "-c", "32768", "--jinja",
    ])
