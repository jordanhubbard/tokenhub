"""
Orchestration engine with adversarial mode support
"""
from typing import List, Optional, Dict
from dataclasses import dataclass
from enum import Enum
from .providers import ChatRequest, ChatResponse, ChatMessage
from .routing import RoutingEngine, RoutingPolicy


class OrchestrationMode(Enum):
    """Orchestration modes."""
    SIMPLE = "simple"  # Direct single model response
    ADVERSARIAL = "adversarial"  # Model A generates, Model B critiques


@dataclass
class OrchestrationConfig:
    """Configuration for orchestration."""
    mode: OrchestrationMode = OrchestrationMode.SIMPLE
    planner_model: Optional[str] = None
    critic_model: Optional[str] = None
    max_refinement_iterations: int = 1
    enable_refinement: bool = False


@dataclass
class OrchestrationResult:
    """Result of orchestration."""
    final_response: ChatResponse
    plan_response: Optional[ChatResponse] = None
    critique_response: Optional[ChatResponse] = None
    refinement_responses: Optional[List[ChatResponse]] = None
    iterations: int = 1


class OrchestrationEngine:
    """
    Engine for orchestrating multi-model interactions with adversarial mode.
    """
    
    def __init__(
        self,
        routing_engine: RoutingEngine,
        default_config: Optional[OrchestrationConfig] = None
    ):
        """
        Initialize the orchestration engine.
        
        Args:
            routing_engine: Routing engine for model selection
            default_config: Default orchestration configuration
        """
        self.routing_engine = routing_engine
        self.default_config = default_config or OrchestrationConfig()
    
    def execute(
        self,
        request: ChatRequest,
        config: Optional[OrchestrationConfig] = None,
        policy: Optional[RoutingPolicy] = None
    ) -> OrchestrationResult:
        """
        Execute a request with the specified orchestration mode.
        
        Args:
            request: Chat request
            config: Optional orchestration configuration
            policy: Optional routing policy
            
        Returns:
            Orchestration result
        """
        config = config or self.default_config
        
        if config.mode == OrchestrationMode.SIMPLE:
            return self._execute_simple(request, policy)
        elif config.mode == OrchestrationMode.ADVERSARIAL:
            return self._execute_adversarial(request, config, policy)
        else:
            raise ValueError(f"Unknown orchestration mode: {config.mode}")
    
    def _execute_simple(
        self,
        request: ChatRequest,
        policy: Optional[RoutingPolicy]
    ) -> OrchestrationResult:
        """
        Execute a simple single-model request.
        
        Args:
            request: Chat request
            policy: Optional routing policy
            
        Returns:
            Orchestration result
        """
        response = self.routing_engine.execute_with_routing(request, policy)
        return OrchestrationResult(
            final_response=response,
            iterations=1
        )
    
    def _execute_adversarial(
        self,
        request: ChatRequest,
        config: OrchestrationConfig,
        policy: Optional[RoutingPolicy]
    ) -> OrchestrationResult:
        """
        Execute adversarial orchestration where one model generates and another critiques.
        
        Args:
            request: Chat request
            config: Orchestration configuration
            policy: Optional routing policy
            
        Returns:
            Orchestration result
        """
        # Step 1: Model A generates plan/response
        plan_request = ChatRequest(
            messages=request.messages.copy(),
            model=config.planner_model,
            max_tokens=request.max_tokens,
            temperature=request.temperature
        )
        plan_response = self.routing_engine.execute_with_routing(plan_request, policy)
        
        # Step 2: Model B critiques
        critique_messages = request.messages.copy()
        critique_messages.append(ChatMessage(
            role="assistant",
            content=plan_response.content
        ))
        critique_messages.append(ChatMessage(
            role="user",
            content="Please provide a critical analysis of the above response. "
                   "Identify any issues, inaccuracies, or areas for improvement."
        ))
        
        critique_request = ChatRequest(
            messages=critique_messages,
            model=config.critic_model,
            max_tokens=request.max_tokens,
            temperature=request.temperature
        )
        critique_response = self.routing_engine.execute_with_routing(critique_request, policy)
        
        # Step 3: Optional refinement loop
        refinement_responses = []
        final_response = plan_response
        
        if config.enable_refinement and config.max_refinement_iterations > 0:
            for i in range(config.max_refinement_iterations):
                refinement_messages = request.messages.copy()
                refinement_messages.append(ChatMessage(
                    role="assistant",
                    content=plan_response.content
                ))
                refinement_messages.append(ChatMessage(
                    role="user",
                    content=f"Critique: {critique_response.content}\n\n"
                           "Please refine your response based on this critique."
                ))
                
                refinement_request = ChatRequest(
                    messages=refinement_messages,
                    model=config.planner_model,
                    max_tokens=request.max_tokens,
                    temperature=request.temperature
                )
                refinement = self.routing_engine.execute_with_routing(refinement_request, policy)
                refinement_responses.append(refinement)
                final_response = refinement
                
                # Update for next iteration
                plan_response = refinement
        
        return OrchestrationResult(
            final_response=final_response,
            plan_response=plan_response,
            critique_response=critique_response,
            refinement_responses=refinement_responses if refinement_responses else None,
            iterations=1 + len(refinement_responses)
        )
    
    def create_adversarial_summary(
        self,
        result: OrchestrationResult
    ) -> Dict[str, str]:
        """
        Create a summary of an adversarial orchestration result.
        
        Args:
            result: Orchestration result
            
        Returns:
            Dictionary with summary information
        """
        summary = {
            "final_response": result.final_response.content,
            "iterations": str(result.iterations)
        }
        
        if result.plan_response:
            summary["initial_plan"] = result.plan_response.content
        
        if result.critique_response:
            summary["critique"] = result.critique_response.content
        
        if result.refinement_responses:
            summary["refinements"] = [r.content for r in result.refinement_responses]
        
        return summary
