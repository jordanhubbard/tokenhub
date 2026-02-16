FROM python:3.11-slim

WORKDIR /app

# Install dependencies
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy application
COPY tokenhub/ ./tokenhub/
COPY setup.py .

# Install the package
RUN pip install -e .

# Create data directory
RUN mkdir -p /data

# Expose port
EXPOSE 8080

# Set environment variables
ENV TOKENHUB_DB_PATH=/data/tokenhub.db
ENV TOKENHUB_HOST=0.0.0.0
ENV TOKENHUB_PORT=8080

# Run the application
CMD ["python", "-m", "tokenhub.main"]
