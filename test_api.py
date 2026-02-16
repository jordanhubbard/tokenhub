"""
Test the Flask API endpoints
"""
import os
import time
import requests
import threading
from tokenhub.api import create_app

def start_server():
    """Start the API server in a thread."""
    api = create_app("test-password", "/tmp/test-tokenhub.db")
    api.run(host='127.0.0.1', port=8080, debug=False)

def test_api():
    """Test the API endpoints."""
    # Give the server time to start
    time.sleep(2)
    
    base_url = "http://127.0.0.1:8080"
    admin_headers = {
        "Authorization": "Bearer test-password",
        "Content-Type": "application/json"
    }
    
    print("Testing TokenHub API...")
    
    # Test health endpoint
    print("\n1. Testing health endpoint...")
    response = requests.get(f"{base_url}/health")
    print(f"   Status: {response.status_code}")
    print(f"   Response: {response.json()}")
    
    # Test adding a provider
    print("\n2. Adding a provider...")
    response = requests.post(
        f"{base_url}/admin/providers",
        headers=admin_headers,
        json={
            "name": "mock",
            "provider_type": "mock",
            "api_key": "test-key-123"
        }
    )
    print(f"   Status: {response.status_code}")
    print(f"   Response: {response.json()}")
    
    # Test listing providers
    print("\n3. Listing providers...")
    response = requests.get(f"{base_url}/admin/providers", headers=admin_headers)
    print(f"   Status: {response.status_code}")
    print(f"   Response: {response.json()}")
    
    # Test chat completion (simple mode)
    print("\n4. Testing chat completion (simple mode)...")
    response = requests.post(
        f"{base_url}/v1/chat",
        json={
            "messages": [
                {"role": "user", "content": "Hello, how are you?"}
            ],
            "orchestration_mode": "simple"
        }
    )
    print(f"   Status: {response.status_code}")
    if response.status_code == 200:
        result = response.json()
        print(f"   Response: {result.get('response')}")
        print(f"   Model: {result.get('model')}")
        print(f"   Tokens: {result.get('tokens_used')}")
    else:
        print(f"   Error: {response.text}")
    
    # Test chat completion (adversarial mode)
    print("\n5. Testing chat completion (adversarial mode)...")
    response = requests.post(
        f"{base_url}/v1/chat",
        json={
            "messages": [
                {"role": "user", "content": "Explain quantum computing"}
            ],
            "orchestration_mode": "adversarial",
            "planner_model": "mock-gpt-4",
            "critic_model": "mock-gpt-3.5"
        }
    )
    print(f"   Status: {response.status_code}")
    if response.status_code == 200:
        result = response.json()
        print(f"   Response: {result.get('response')}")
        print(f"   Adversarial details: {bool(result.get('adversarial'))}")
    else:
        print(f"   Error: {response.text}")
    
    # Test metrics endpoint
    print("\n6. Testing metrics endpoint...")
    response = requests.get(f"{base_url}/metrics")
    print(f"   Status: {response.status_code}")
    metrics_lines = response.text.split('\n')
    print(f"   Sample metrics (first 10 lines):")
    for line in metrics_lines[:10]:
        if line and not line.startswith('#'):
            print(f"     {line}")
    
    print("\n✓ All API tests completed successfully!")

if __name__ == "__main__":
    # Start server in a thread
    server_thread = threading.Thread(target=start_server, daemon=True)
    server_thread.start()
    
    # Run tests
    try:
        test_api()
    except Exception as e:
        print(f"\n✗ Test failed: {e}")
        import traceback
        traceback.print_exc()
    finally:
        # Give some time for logging
        time.sleep(1)
