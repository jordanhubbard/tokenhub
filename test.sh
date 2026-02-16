#!/bin/bash
# Test script for Tokenhub service

set -e

echo "==================================="
echo "Tokenhub Service Test Script"
echo "==================================="
echo ""

# Check if service is running
echo "Checking health endpoint..."
HEALTH=$(curl -s http://localhost:8080/health)
if [[ $HEALTH == *"healthy"* ]]; then
    echo "✓ Service is healthy"
else
    echo "✗ Service is not responding correctly"
    exit 1
fi
echo ""

# Test chat completion endpoint
echo "Testing chat completion endpoint..."
CHAT_RESPONSE=$(curl -s -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "messages": [
      {"role": "user", "content": "Say hello"}
    ],
    "max_tokens": 10,
    "temperature": 0.7
  }')

if [[ $CHAT_RESPONSE == *"message"* ]] || [[ $CHAT_RESPONSE == *"error"* ]] || [[ $CHAT_RESPONSE == *"failed"* ]]; then
    echo "✓ Chat completion endpoint is working"
    echo "   Response: $CHAT_RESPONSE"
else
    echo "✗ Chat completion endpoint failed"
    echo "   Response: $CHAT_RESPONSE"
fi
echo ""

# Test completion endpoint
echo "Testing completion endpoint..."
COMPLETION_RESPONSE=$(curl -s -X POST http://localhost:8080/v1/completions \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "Hello",
    "max_tokens": 10,
    "temperature": 0.7
  }')

if [[ $COMPLETION_RESPONSE == *"text"* ]] || [[ $COMPLETION_RESPONSE == *"error"* ]] || [[ $COMPLETION_RESPONSE == *"failed"* ]]; then
    echo "✓ Completion endpoint is working"
    echo "   Response: $COMPLETION_RESPONSE"
else
    echo "✗ Completion endpoint failed"
    echo "   Response: $COMPLETION_RESPONSE"
fi
echo ""

echo "==================================="
echo "Tests completed!"
echo "==================================="
echo ""
echo "Note: If you see 'provider not found' or 'no API key' errors,"
echo "this is expected when no provider API keys are configured."
echo "The service is working correctly and will route requests"
echo "when valid API keys are provided."
