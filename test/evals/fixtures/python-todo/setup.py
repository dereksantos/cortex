from setuptools import find_packages, setup

setup(
    name="python-todo",
    version="0.1.0",
    description="Tiny Flask todo API used as a Cortex eval fixture.",
    packages=find_packages(exclude=["tests"]),
    python_requires=">=3.11",
    install_requires=["flask>=3.0"],
)
