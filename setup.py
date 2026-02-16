from setuptools import setup, find_packages

setup(
    name="tokenhub",
    version="0.1.0",
    description="Organize your token providers by cost, complexity, and reliability",
    packages=find_packages(),
    python_requires=">=3.8",
    install_requires=[
        "flask>=3.0.0",
        "cryptography>=41.0.7",
        "prometheus-client>=0.19.0",
        "requests>=2.31.0",
        "pydantic>=2.5.0",
        "python-dotenv>=1.0.0",
    ],
)
