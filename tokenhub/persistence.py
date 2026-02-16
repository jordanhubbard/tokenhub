"""
Persistence layer using SQLite
"""
import sqlite3
import json
from typing import Optional, Dict, Any
from contextlib import contextmanager


class PersistenceManager:
    """
    Manages persistence of configuration and state using SQLite.
    """
    
    def __init__(self, db_path: str = "tokenhub.db"):
        """
        Initialize the persistence manager.
        
        Args:
            db_path: Path to SQLite database file
        """
        self.db_path = db_path
        self._init_database()
    
    @contextmanager
    def _get_connection(self):
        """Get a database connection context."""
        conn = sqlite3.connect(self.db_path)
        conn.row_factory = sqlite3.Row
        try:
            yield conn
        finally:
            conn.close()
    
    def _init_database(self) -> None:
        """Initialize database schema."""
        with self._get_connection() as conn:
            cursor = conn.cursor()
            
            # Vault data table
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS vault_data (
                    id INTEGER PRIMARY KEY,
                    salt TEXT NOT NULL,
                    data TEXT NOT NULL,
                    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
                )
            """)
            
            # Model registry table
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS models (
                    name TEXT PRIMARY KEY,
                    provider TEXT NOT NULL,
                    cost_per_1k_tokens REAL NOT NULL,
                    context_size INTEGER NOT NULL,
                    weight INTEGER NOT NULL,
                    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
                )
            """)
            
            # Metrics table
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS metrics (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
                    event_type TEXT NOT NULL,
                    data TEXT NOT NULL
                )
            """)
            
            conn.commit()
    
    def save_vault_data(self, vault_data: Dict) -> None:
        """
        Save vault data to database.
        
        Args:
            vault_data: Encrypted vault data
        """
        with self._get_connection() as conn:
            cursor = conn.cursor()
            
            # Delete existing data
            cursor.execute("DELETE FROM vault_data")
            
            # Insert new data
            cursor.execute(
                "INSERT INTO vault_data (salt, data) VALUES (?, ?)",
                (vault_data["salt"], json.dumps(vault_data["data"]))
            )
            
            conn.commit()
    
    def load_vault_data(self) -> Optional[Dict]:
        """
        Load vault data from database.
        
        Returns:
            Vault data or None if not found
        """
        with self._get_connection() as conn:
            cursor = conn.cursor()
            cursor.execute("SELECT salt, data FROM vault_data ORDER BY id DESC LIMIT 1")
            row = cursor.fetchone()
            
            if row:
                return {
                    "salt": row["salt"],
                    "data": json.loads(row["data"])
                }
            return None
    
    def save_models(self, models_data: Dict) -> None:
        """
        Save model registry data to database.
        
        Args:
            models_data: Dictionary of model metadata
        """
        with self._get_connection() as conn:
            cursor = conn.cursor()
            
            for name, model in models_data.items():
                cursor.execute("""
                    INSERT OR REPLACE INTO models
                    (name, provider, cost_per_1k_tokens, context_size, weight)
                    VALUES (?, ?, ?, ?, ?)
                """, (
                    name,
                    model["provider"],
                    model["cost_per_1k_tokens"],
                    model["context_size"],
                    model["weight"]
                ))
            
            conn.commit()
    
    def load_models(self) -> Dict:
        """
        Load model registry data from database.
        
        Returns:
            Dictionary of model metadata
        """
        with self._get_connection() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                SELECT name, provider, cost_per_1k_tokens, context_size, weight
                FROM models
            """)
            
            models = {}
            for row in cursor.fetchall():
                models[row["name"]] = {
                    "name": row["name"],
                    "provider": row["provider"],
                    "cost_per_1k_tokens": row["cost_per_1k_tokens"],
                    "context_size": row["context_size"],
                    "weight": row["weight"]
                }
            
            return models
    
    def log_metric(self, event_type: str, data: Dict[str, Any]) -> None:
        """
        Log a metric event.
        
        Args:
            event_type: Type of event
            data: Event data
        """
        with self._get_connection() as conn:
            cursor = conn.cursor()
            cursor.execute(
                "INSERT INTO metrics (event_type, data) VALUES (?, ?)",
                (event_type, json.dumps(data))
            )
            conn.commit()
    
    def get_metrics(self, limit: int = 100) -> list:
        """
        Get recent metrics.
        
        Args:
            limit: Maximum number of metrics to return
            
        Returns:
            List of metric records
        """
        with self._get_connection() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                SELECT timestamp, event_type, data
                FROM metrics
                ORDER BY timestamp DESC
                LIMIT ?
            """, (limit,))
            
            return [
                {
                    "timestamp": row["timestamp"],
                    "event_type": row["event_type"],
                    "data": json.loads(row["data"])
                }
                for row in cursor.fetchall()
            ]
