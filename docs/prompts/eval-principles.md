# Eval Principles

All of our evals as a principle and constrain MUST use the `cortex` cli tool and treat it as a black box. Otherwise, evals are useless because they will be coached by the code that wraps the eval runner. This is a major problem in cortex right now and stunts progress significantly.

## Evals must treat cortex as a black box, and use its cli as the public interface

No running evals with explicit internal wrappers. They must cortex as is, as intended through the same cli interface developers and agents would use. Otherwise, we are not truly testing the right thing.

## Evals must not be coached

An eval should be isolated, startinng with a clean slate every time for the task, unless the benchmark explicitly is designed to include pre-existing context. Any specific wrappers around an eval that may coach the results degrade the data the eval was designed to provide.

## Evals must be graded fairly and honestly

Do not fudge the numbers or tamper the results such that the value of the eval is attenuated. Evals must be run objectively, with the goal of providing trusthworthy data to the developers.