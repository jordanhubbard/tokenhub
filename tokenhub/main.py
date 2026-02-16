"""
Main entry point for TokenHub
"""
import os
import sys
from .api import create_app


def main():
    """Main entry point."""
    # Get configuration from environment
    admin_password = os.getenv('TOKENHUB_ADMIN_PASSWORD')
    if not admin_password:
        print("ERROR: TOKENHUB_ADMIN_PASSWORD environment variable must be set", file=sys.stderr)
        sys.exit(1)
    
    host = os.getenv('TOKENHUB_HOST', '0.0.0.0')
    port = int(os.getenv('TOKENHUB_PORT', '8080'))
    db_path = os.getenv('TOKENHUB_DB_PATH', 'tokenhub.db')
    debug = os.getenv('TOKENHUB_DEBUG', 'false').lower() == 'true'
    
    # Create and run app
    print(f"Starting TokenHub on {host}:{port}")
    api = create_app(admin_password, db_path)
    api.run(host=host, port=port, debug=debug)


if __name__ == '__main__':
    main()
