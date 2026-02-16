#!/usr/bin/env python
"""
TokenHub Demo - Showcases all key features
"""
import json
from tokenhub.api import create_app
from tokenhub.providers import ChatRequest, ChatMessage
from tokenhub.orchestration import OrchestrationConfig, OrchestrationMode
from tokenhub.models import ModelMetadata

print("=" * 70)
print("TokenHub - AI Model Router & Orchestrator Demo")
print("=" * 70)

# Initialize
print("\n📦 Initializing TokenHub...")
api = create_app("demo-password", "/tmp/demo-tokenhub.db")

# Add provider
print("\n🔌 Adding mock provider...")
api.provider_registry.add_provider(
    name="mock",
    provider_type="mock",
    api_key="demo-key-123"
)
print("   ✓ Provider 'mock' registered")

# Show model registry
print("\n📊 Model Registry:")
models = api.model_registry.list_models()
for model in models:
    print(f"   • {model.name}")
    print(f"     Provider: {model.provider}")
    print(f"     Cost: ${model.cost_per_1k_tokens}/1k tokens")
    print(f"     Context: {model.context_size:,} tokens")
    print(f"     Weight: {model.weight}")
    print()

# Test 1: Simple mode
print("=" * 70)
print("Test 1: Simple Chat Mode")
print("=" * 70)
request = ChatRequest(
    messages=[ChatMessage(role="user", content="What is the capital of France?")]
)
result = api.orchestration_engine.execute(request)
print(f"\n💬 User: What is the capital of France?")
print(f"🤖 Assistant ({result.final_response.model}): {result.final_response.content}")
print(f"📈 Tokens used: {result.final_response.tokens_used}")

# Test 2: Adversarial mode
print("\n" + "=" * 70)
print("Test 2: Adversarial Orchestration Mode")
print("=" * 70)
request = ChatRequest(
    messages=[ChatMessage(
        role="user",
        content="Explain how blockchain technology works"
    )]
)
config = OrchestrationConfig(
    mode=OrchestrationMode.ADVERSARIAL,
    planner_model="mock-gpt-4",
    critic_model="mock-gpt-3.5"
)
result = api.orchestration_engine.execute(request, config)
print(f"\n💬 User: Explain how blockchain technology works")
print(f"\n🤖 Planner ({result.plan_response.model}):")
print(f"   {result.plan_response.content}")
print(f"\n🔍 Critic ({result.critique_response.model}):")
print(f"   {result.critique_response.content}")
print(f"\n✅ Final Response:")
print(f"   {result.final_response.content}")
print(f"\n📈 Total iterations: {result.iterations}")

# Test 3: Routing with policies
print("\n" + "=" * 70)
print("Test 3: Cost-Optimized Routing")
print("=" * 70)
from tokenhub.routing import RoutingPolicy

policy = RoutingPolicy(
    max_cost_per_1k_tokens=0.01,
    prefer_higher_weight=False  # Prefer cheaper models
)

selected = api.routing_engine.select_model(request, policy)
print(f"\n🎯 Selected model: {selected.name}")
print(f"   Cost: ${selected.cost_per_1k_tokens}/1k")
print(f"   Reason: Cost-optimized (≤ $0.01/1k)")

# Test 4: Vault operations
print("\n" + "=" * 70)
print("Test 4: Secure Vault Operations")
print("=" * 70)
vault_keys = api.vault.list_keys()
print(f"\n🔐 Encrypted keys in vault: {len(vault_keys)}")
for key in vault_keys:
    print(f"   • {key}")

# Summary
print("\n" + "=" * 70)
print("✨ Demo Complete!")
print("=" * 70)
print("\nKey Features Demonstrated:")
print("  ✓ Provider registry with encrypted API keys")
print("  ✓ Model registry with cost and context tracking")
print("  ✓ Simple orchestration mode")
print("  ✓ Adversarial orchestration mode")
print("  ✓ Intelligent routing with policies")
print("  ✓ Secure vault operations")
print("\nNext Steps:")
print("  • Start the API: python -m tokenhub.main")
print("  • Add real providers (OpenAI, Anthropic, etc.)")
print("  • Configure routing policies for your use case")
print("  • Monitor with Prometheus metrics")
print("\n" + "=" * 70)
